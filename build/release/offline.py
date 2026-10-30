#!/usr/bin/env python3
"""Check an unsigned PHPM material directory and sign it on an offline Windows/Linux amd64 device."""
import argparse
import base64
import hashlib
import itertools
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import stat
import subprocess
import tempfile

SHA = re.compile(r"[a-f0-9]{64}")
VERSION = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?")
NOTICES = ("LICENSE", "THIRD_PARTY_NOTICES.md", "third_party/go/LICENSE", "third_party/yaml.v3/LICENSE", "third_party/yaml.v3/Apache-2.0.txt")


def hash_file(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 512 * 1024 * 1024:
        raise ValueError("material must be a nonempty regular file within 512 MiB: " + str(path))
    digest, size = hashlib.sha256(), 0
    with path.open("rb") as source:
        for data in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(data)
            size += len(data)
            if size > 512 * 1024 * 1024:
                raise ValueError("material grew beyond limit")
    return {"size": size, "sha256": digest.hexdigest()}


def unique_pairs(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ValueError("duplicate request JSON key")
        result[key] = value
    return result


def safe_path(name):
    if not isinstance(name, str) or len(name) > 240 or "\\" in name:
        raise ValueError("invalid material path")
    path = PurePosixPath(name)
    if path.is_absolute() or path.as_posix() != name or any(not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", part) or ".." in part for part in path.parts):
        raise ValueError("unsafe material path")
    return path


def inspect_materials(directory, expected):
    directory = directory.resolve()
    request_file = directory / "release-inputs.json"
    if not stat.S_ISREG(request_file.lstat().st_mode):
        raise ValueError("request must be a regular file")
    with request_file.open("rb") as source:
        raw = source.read(4 * 1024 * 1024 + 1)
    if len(raw) > 4 * 1024 * 1024 or not SHA.fullmatch(expected) or hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("release request SHA-256 mismatch; use the hash from the reviewed build run")
    value = json.loads(raw, object_pairs_hook=unique_pairs)
    if (set(value) != {"schema", "version", "commit", "repository", "source_runs", "files"} or value["schema"] != 1
            or not VERSION.fullmatch(value["version"]) or len(value["version"]) > 64
            or not re.fullmatch(r"[a-f0-9]{40}", value["commit"])
            or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", value["repository"])
            or not isinstance(value["files"], dict) or not 1 <= len(value["files"]) <= 128):
        raise ValueError("invalid release request identity/files")
    required = {"clients/linux-amd64/phpm", "clients/windows-amd64/phpm.exe", "tools/linux-amd64/phpm-release",
                "tools/windows-amd64/phpm-release.exe", "install-phpm.py", "offline-sign.py"}
    required.update("notices/" + name for name in NOTICES)
    if not required.issubset(value["files"]):
        raise ValueError("missing release client/tool/installer/notices")
    for name, expected_asset in value["files"].items():
        path = directory.joinpath(*safe_path(name).parts)
        # Reject links in all ancestors, including a linked input directory.
        for parent in (path, *path.parents):
            if parent == directory:
                break
            if parent.is_symlink():
                raise ValueError("material links are forbidden")
        if hash_file(path) != expected_asset:
            raise ValueError("material hash/size mismatch: " + name)
    actual = set()
    for index, entry in enumerate(itertools.islice(directory.rglob("*"), 1025)):
        if index == 1024:
            raise ValueError("too many material directory entries")
        mode = entry.lstat().st_mode
        if stat.S_ISDIR(mode):
            continue
        if not stat.S_ISREG(mode):
            raise ValueError("material links/special files are forbidden")
        actual.add(entry.relative_to(directory).as_posix())
    if actual != set(value["files"]) | {"release-inputs.json"}:
        raise ValueError("unsigned material directory contains unexpected or missing files")
    return value


def maintainer_tool(directory):
    if platform.machine().lower() not in ("x86_64", "amd64") or platform.system() not in ("Windows", "Linux"):
        raise ValueError("offline signing tools support Windows/Linux amd64")
    target = "windows-amd64/phpm-release.exe" if os.name == "nt" else "linux-amd64/phpm-release"
    result = directory / "tools" / target
    if os.name != "nt":
        result.chmod(0o755)
    return result


def build_directories(directory):
    """The release may add a Debian 12 pair without replacing the existing pair."""
    inputs = directory / "inputs"
    roles = {path.name for path in inputs.iterdir()}
    allowed = {"base", "target"}
    if roles == allowed | {"debian12-base", "debian12-target"}:
        allowed = roles
    if roles != allowed or any(not (inputs / role).is_dir() or (inputs / role).is_symlink() for role in roles):
        raise ValueError("release requires a complete base/target pair and optional complete Debian 12 pair")
    return [inputs / role for role in sorted(roles)]


def sign(args):
    directory = args.input.resolve()
    request = inspect_materials(directory, args.request_sha256)
    with args.root.open("rb") as source:
        raw = source.read(1025)
    root = base64.b64decode(raw.strip(), validate=True)
    if len(raw) > 1024 or len(root) != 32 or not SHA.fullmatch(args.fingerprint) or hashlib.sha256(root).hexdigest() != args.fingerprint:
        raise ValueError("independently checked root fingerprint mismatch")
    if args.sequence < 1:
        raise ValueError("catalog sequence must be positive and higher than previous releases")
    output = args.output.absolute()
    if output.exists() or output.is_symlink():
        raise ValueError("signed output directory must not already exist")
    # Keep generated output, authorization and private key paths outside the
    # allowlisted material directory; the manifest never includes private data.
    if output.is_relative_to(directory) or args.key.resolve().is_relative_to(directory):
        raise ValueError("output/private keys must be outside the unsigned material directory")
    tool = maintainer_tool(directory)
    builds = build_directories(directory)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".phpm-offline-sign-", dir=output.parent) as temporary:
        work = Path(temporary)
        subprocess.run([str(tool), "catalog", "--key", str(args.key.resolve()), "--root", str(args.root.resolve()),
            "--trust", str(args.trust.resolve()), "--sequence", str(args.sequence), "--validity", args.validity,
            "--output", str(work / "php"), *map(str, builds)], check=True)
        subprocess.run([str(tool), "clients", "--bundle", str(work / "php"), "--root", str(args.root.resolve()),
            "--key", str(args.key.resolve()), "--version", request["version"], "--commit", request["commit"],
            "--linux", str(directory / "clients/linux-amd64/phpm"), "--windows", str(directory / "clients/windows-amd64/phpm.exe"),
            "--notices", str(directory / "notices"), "--installer", str(directory / "install-phpm.py"),
            "--validity", args.validity, "--output", str(work / "release")], check=True)
        subprocess.run([str(tool), "verify", "--bundle", str(work / "release"), "--root", str(args.root.resolve()),
                        "--fingerprint", args.fingerprint], check=True)
        if output.exists() or output.is_symlink():
            raise ValueError("signed output destination appeared; nothing was replaced")
        (work / "release").rename(output)
    return {"status": "offline-signed", "output": str(output), "version": request["version"], "commit": request["commit"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("check", "sign"):
        command = commands.add_parser(name)
        command.add_argument("--input", type=Path, required=True)
        command.add_argument("--request-sha256", required=True, help="independently recorded build request hash")
        if name == "sign":
            command.add_argument("--root", type=Path, required=True)
            command.add_argument("--fingerprint", required=True)
            command.add_argument("--trust", type=Path, required=True, help="root-signed public authorization directory")
            command.add_argument("--key", type=Path, required=True, help="offline publishing seed file, never uploaded")
            command.add_argument("--sequence", type=int, required=True)
            command.add_argument("--validity", default="720h")
            command.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "check":
            result = inspect_materials(args.input, args.request_sha256)
            print(json.dumps({"status": "materials-verified", "version": result["version"], "commit": result["commit"], "files": len(result["files"])}))
        else:
            print(json.dumps(sign(args)))
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, "phpm offline signing: " + str(error) + "\n")


if __name__ == "__main__":
    main()
