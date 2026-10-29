#!/usr/bin/env python3
"""Build allowlisted unsigned materials; no formal private key is used by this build job."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from offline import hash_file, inspect_materials, NOTICES, VERSION, safe_path

PROJECT = Path(__file__).resolve().parents[2]


def run(args, **kwargs):
    subprocess.run(list(map(str, args)), check=True, cwd=PROJECT, **kwargs)


def prepare(args):
    if not VERSION.fullmatch(args.version) or len(args.version) > 64:
        raise ValueError("invalid candidate version")
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=PROJECT, text=True).strip()
    if subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=normal"], cwd=PROJECT, text=True).strip():
        raise ValueError("prepare requires a clean source checkout")
    output = args.output.absolute()
    if output.exists() or output.is_symlink():
        raise ValueError("material destination already exists")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".phpm-prepare-", dir=output.parent) as temporary:
        work = Path(temporary)
        stage = work / "materials"
        stage.mkdir()
        env = dict(os.environ, CGO_ENABLED="0", GOAMD64="v1")
        for target in ("linux", "windows"):
            suffix = ".exe" if target == "windows" else ""
            for command, kind in (("phpm", "clients"), ("phpm-release", "tools")):
                destination = stage / kind / (target + "-amd64") / (command + suffix)
                destination.parent.mkdir(parents=True, exist_ok=True)
                flags = ["-ldflags", "-X phpm/internal/phpm.Version=" + args.version] if command == "phpm" else []
                run(["go", "build", "-trimpath", *flags, "-o", destination, "./cmd/" + command], env=dict(env, GOOS=target, GOARCH="amd64"))
        native = "windows-amd64" if os.name == "nt" else "linux-amd64"
        suffix = ".exe" if os.name == "nt" else ""
        tool = stage / "tools" / native / ("phpm-release" + suffix)
        actual_version = subprocess.check_output([str(stage / "clients" / native / ("phpm" + suffix)), "version"], text=True).strip()
        if actual_version != args.version:
            raise ValueError("built client version differs from requested candidate")
        # Existing stage-A validation is reused with a discarded TEST seed. No
        # signature or private seed from this check is copied to the materials.
        run([tool, "keygen", "--private", work / "check.seed", "--public", work / "check.pub"], stdout=subprocess.DEVNULL)
        run([tool, "catalog", "--key", work / "check.seed", "--sequence", "1", "--validity", "1h",
             "--output", work / "checked", args.base.resolve(), args.target.resolve()])
        (work / "check.seed").unlink()
        for role, source in (("base", args.base), ("target", args.target)):
            report = json.loads((source / "build-result.json").read_text())
            names = ["build-result.json", "sdk-verification.json", "verification/runtime-verification.json"]
            names += [item["filename"] for item in report["artifacts"]]
            for name in names:
                original = source.joinpath(*safe_path(name).parts)
                hash_file(original)
                destination = stage / "inputs" / role / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(original, destination)
        for name in NOTICES:
            destination = stage / "notices" / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(PROJECT / name, destination)
        shutil.copyfile(PROJECT / "scripts/install-phpm.py", stage / "install-phpm.py")
        shutil.copyfile(Path(__file__).with_name("offline.py"), stage / "offline-sign.py")
        shutil.copyfile(PROJECT / "docs/release-quickstart.zh-CN.md", stage / "OFFLINE-GUIDE.md")
        provenance = json.loads(args.provenance.read_text())
        if provenance["repository"] != args.repo or len(provenance["source_runs"]) != 2:
            raise ValueError("source provenance does not match the requested repository/build pair")
        request = {"schema": 1, "version": args.version, "commit": commit, "repository": args.repo,
                   "source_runs": provenance["source_runs"], "files": {}}
        for path in sorted(stage.rglob("*")):
            if path.is_file():
                request["files"][path.relative_to(stage).as_posix()] = hash_file(path)
        raw = (json.dumps(request, indent=2, sort_keys=True) + "\n").encode()
        (stage / "release-inputs.json").write_bytes(raw)
        digest = hashlib.sha256(raw).hexdigest()
        inspect_materials(stage, digest)
        stage.rename(output)
    print(json.dumps({"status": "unsigned-materials-ready", "version": args.version, "commit": commit,
                      "request_sha256": digest, "output": str(output), "formal_private_keys_used": False}))
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
            summary.write(f"Candidate: `{args.version}`\n\nSource: `{commit}`\n\nRelease request SHA-256: `{digest}`\n\nUnsigned materials only; no formal private key is used by this build job.\n")
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as outputs:
            outputs.write(f"request_sha256={digest}\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--target", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--provenance", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    prepare(parser.parse_args())


if __name__ == "__main__":
    main()
