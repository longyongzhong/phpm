#!/usr/bin/env python3
"""Install a signed PHPM client into a NEW user-selected directory; no elevation or shell profile edits.

Run this installer from reviewed source. Independently verify the offline root
fingerprint; a public key downloaded beside a release is not a trust decision.
Requires Python 3.10+ and OpenSSL 3 with Ed25519 support. No PHP/Go is required.
"""

import argparse
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import ssl
import stat
import subprocess
import tarfile
import tempfile
import time
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, Request, build_opener
import zipfile

MAX_METADATA = 4 * 1024 * 1024
SHA = re.compile(r"^[a-f0-9]{64}$")
NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$")


def pairs(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def document(data, fields):
    if len(data) > MAX_METADATA:
        raise ValueError("metadata exceeds size limit")
    value = json.loads(data, object_pairs_hook=pairs)
    if not isinstance(value, dict) or set(value) != set(fields):
        raise ValueError("unexpected metadata fields")
    return value


def public(data):
    value = base64.b64decode(data.strip(), validate=True)
    if len(value) != 32:
        raise ValueError("expected a 32-byte Ed25519 public key")
    return value


def digest(data):
    return hashlib.sha256(data).hexdigest()


def dates(value, maximum_days):
    now = datetime.now(timezone.utc)
    issued = datetime.fromisoformat(value["issued_at"].replace("Z", "+00:00"))
    expires = datetime.fromisoformat(value["expires_at"].replace("Z", "+00:00"))
    if (issued.tzinfo is None or expires.tzinfo is None or issued > now + timedelta(minutes=5)
            or expires <= now or expires <= issued or expires - issued > timedelta(days=maximum_days)):
        raise ValueError("metadata expired or has invalid dates")
    return expires


def verify(data, encoded, key, role, version, openssl):
    sig = document(encoded, ("schema", "role", "key_id", "trust_version", "signature"))
    if len(encoded) > 4096 or sig["schema"] != 1 or sig["role"] != role or sig["key_id"] != digest(key) or sig["trust_version"] != version:
        raise ValueError("signature role/key/authorization-version mismatch")
    value = base64.b64decode(sig["signature"], validate=True)
    if len(value) != 64:
        raise ValueError("invalid Ed25519 signature size")
    message = f"PHPM-SIGNATURE-v1\n{role}\n{digest(key)}\n{version}\n".encode() + data
    with tempfile.TemporaryDirectory(prefix="phpm-verify-") as temp:
        root = Path(temp)
        (root / "key.der").write_bytes(bytes.fromhex("302a300506032b6570032100") + key)
        (root / "message").write_bytes(message)
        (root / "signature").write_bytes(value)
        result = subprocess.run([openssl, "pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", str(root / "key.der"),
            "-rawin", "-in", str(root / "message"), "-sigfile", str(root / "signature")], capture_output=True, timeout=30)
        if result.returncode:
            raise ValueError("Ed25519 signature verification failed")


class Redirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, message, headers, location):
        parsed = urlsplit(location)
        if parsed.scheme != "https" or parsed.username is not None:
            raise ValueError("redirect must preserve HTTPS without embedded credentials")
        return super().redirect_request(req, fp, code, message, headers, location)


class Source:
    def __init__(self, location, ca=None):
        self.remote = location.startswith("https://")
        if self.remote:
            parsed = urlsplit(location)
            if not parsed.hostname or parsed.username is not None or parsed.query or parsed.fragment or ".." in parsed.path or "\\" in parsed.path:
                raise ValueError("invalid HTTPS release directory URL")
            self.location = location.rstrip("/") + "/"
        else:
            if "://" in location:
                raise ValueError("release source must be a local directory or HTTPS URL")
            self.location = Path(location).resolve()
        self.client = build_opener(Redirects(), HTTPSHandler(context=ssl.create_default_context(cafile=ca)))

    def read(self, name, limit, byte_range=None):
        if not NAME.fullmatch(name) or ".." in name:
            raise ValueError("unsafe release filename")
        if not self.remote:
            path = self.location / name
            if not stat.S_ISREG(path.lstat().st_mode):
                raise ValueError("release files must be regular files")
            with path.open("rb") as source:
                data = source.read(limit + 1)
        else:
            headers = {"User-Agent": "PHPM-bootstrap", "Accept-Encoding": "identity"}
            if byte_range:
                headers["Range"] = f"bytes={byte_range[0]}-{byte_range[1]}"
            with self.client.open(Request(self.location + name, headers=headers), timeout=60) as response:
                if response.headers.get("Content-Encoding", "identity") != "identity":
                    raise ValueError("unexpected HTTP content encoding")
                if byte_range:
                    first, last, total = byte_range
                    last = min(last, total - 1)
                    if response.status != 206 or response.headers.get("Content-Range") != f"bytes {first}-{last}/{total}":
                        raise ValueError("server does not support the exact required byte range")
                data = response.read(limit + 1)
        if len(data) > limit:
            raise ValueError("release file exceeds size limit")
        return data

    def package(self, asset, destination):
        hasher = hashlib.sha256()
        size = asset["size"]
        with destination.open("xb") as out:
            if not self.remote:
                data = self.read(asset["filename"], size)
                out.write(data)
                hasher.update(data)
                offset = len(data)
            else:
                offset = 0
                while offset < size:
                    length = min(1024 * 1024, size - offset)
                    data = self.read(asset["filename"], length, (offset, offset + length - 1, size))
                    if len(data) != length:
                        raise ValueError("truncated client download; retry the installer")
                    out.write(data)
                    hasher.update(data)
                    offset += len(data)
        if offset != size or hasher.hexdigest() != asset["sha256"]:
            raise ValueError("client package size/SHA-256 mismatch")


def asset(value):
    if not isinstance(value, dict) or set(value) != {"filename", "sha256", "size"}:
        raise ValueError("invalid release asset fields")
    if (not NAME.fullmatch(value["filename"]) or ".." in value["filename"] or not SHA.fullmatch(value["sha256"])
            or type(value["size"]) is not int or not 0 < value["size"] <= 512 * 1024 * 1024):
        raise ValueError("invalid release asset name/hash/size")
    return value


def extract(archive, destination, target):
    binary = "phpm.exe" if target == "windows-amd64" else "phpm"
    required = {binary, "LICENSE", "THIRD_PARTY_NOTICES.md", "third_party/go/LICENSE", "third_party/yaml.v3/LICENSE", "third_party/yaml.v3/Apache-2.0.txt"}
    seen, total = set(), 0

    def write(name, size, stream):
        nonlocal total
        path = PurePosixPath(name)
        if name not in required or name in seen or path.is_absolute() or ".." in path.parts or size < 0:
            raise ValueError("unexpected, duplicate or unsafe client archive entry")
        seen.add(name)
        total += size
        if total > 128 * 1024 * 1024 or len(seen) > 10:
            raise ValueError("expanded client archive exceeds limits")
        output = destination.joinpath(*path.parts)
        output.parent.mkdir(parents=True, exist_ok=True)
        with output.open("xb") as out:
            left = size
            while left:
                data = stream.read(min(left, 65536))
                if not data:
                    raise ValueError("truncated client archive entry")
                out.write(data)
                left -= len(data)
        output.chmod(0o755 if name == binary else 0o644)

    if target == "windows-amd64":
        with zipfile.ZipFile(archive) as source:
            if len(source.infolist()) > 10:
                raise ValueError("too many client archive entries")
            for item in source.infolist():
                mode = (item.external_attr >> 16) & 0o170000
                if item.is_dir() or mode not in (0, stat.S_IFREG):
                    raise ValueError("client archive contains a link or special file")
                with source.open(item) as stream:
                    write(item.filename, item.file_size, stream)
    else:
        with tarfile.open(archive, "r:gz") as source:
            for item in source:
                if not item.isfile():
                    raise ValueError("client archive contains a link or special file")
                with source.extractfile(item) as stream:
                    write(item.name, item.size, stream)
    if seen != required:
        raise ValueError("client package is missing its binary or required notices")
    return destination / binary


def commit_install(payload, destination):
    # Windows file scanners can briefly hold freshly written executable files.
    # Retry only the final rename, never validation or overwriting a destination.
    for attempt in range(16):
        if destination.exists() or destination.is_symlink():
            raise ValueError("destination appeared during installation; nothing was replaced")
        try:
            payload.rename(destination)
            return
        except PermissionError as error:
            if os.name != "nt" or error.winerror not in (5, 32, 33) or attempt == 15:
                raise
            time.sleep(0.2)


def install(args):
    if args.dest.is_symlink():
        raise ValueError("destination must not be a symbolic link")
    destination = args.dest.resolve()
    if destination.exists() or destination.is_symlink():
        raise ValueError("destination must not already exist")
    with args.root.open("rb") as stream:
        root_bytes = stream.read(1025)
    if len(root_bytes) > 1024:
        raise ValueError("root public key file exceeds limit")
    root = public(root_bytes)
    if not SHA.fullmatch(args.fingerprint.lower()) or digest(root) != args.fingerprint.lower():
        raise ValueError("independently checked root fingerprint mismatch")
    source = Source(args.source, args.ca)
    raw = source.read("trust.json", MAX_METADATA)
    trust = document(raw, ("schema", "version", "root_sha256", "issued_at", "expires_at", "keys", "revoked"))
    if trust["schema"] != 1 or type(trust["version"]) is not int or not 0 < trust["version"] < 2**64 or trust["root_sha256"] != digest(root):
        raise ValueError("invalid root authorization identity")
    trust_expires = dates(trust, 365)
    verify(raw, source.read("trust.json.sig", 4096), root, "trust", trust["version"], args.openssl)
    if not isinstance(trust["keys"], list) or len(trust["keys"]) > 16 or not isinstance(trust["revoked"], list) or len(trust["revoked"]) > 256:
        raise ValueError("invalid authorization key limits")
    keys = {}
    for item in trust["keys"]:
        if set(item) != {"id", "public_key"}:
            raise ValueError("invalid authorized key fields")
        key = public(item["public_key"].encode())
        if item["id"] != digest(key) or item["id"] in keys or item["id"] == digest(root):
            raise ValueError("invalid/duplicate authorized publishing key")
        keys[item["id"]] = key
    if len(set(trust["revoked"])) != len(trust["revoked"]) or any(not SHA.fullmatch(i) or i == digest(root) for i in trust["revoked"]):
        raise ValueError("invalid revoked key list")
    raw = source.read("clients.json", MAX_METADATA)
    encoded = source.read("clients.json.sig", 4096)
    signature = document(encoded, ("schema", "role", "key_id", "trust_version", "signature"))
    key_id = signature["key_id"]
    if key_id in trust["revoked"] or key_id not in keys:
        raise ValueError("client publishing key is revoked or unauthorized")
    verify(raw, encoded, keys[key_id], "clients", trust["version"], args.openssl)
    manifest = document(raw, ("schema", "version", "commit", "issued_at", "expires_at", "clients", "files"))
    if (manifest["schema"] != 1 or not re.fullmatch(r"[a-f0-9]{40}", manifest["commit"])
            or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?", manifest["version"])):
        raise ValueError("invalid client release identity")
    if dates(manifest, 90) > trust_expires or (args.version and manifest["version"] != args.version):
        raise ValueError("client version/authorization lifetime mismatch")
    files = {}
    if not 0 < len(manifest["files"]) <= 4000 or not 0 < len(manifest["clients"]) <= 2:
        raise ValueError("invalid release file/platform count")
    for value in manifest["files"]:
        value = asset(value)
        if value["filename"] in files:
            raise ValueError("duplicate release asset")
        files[value["filename"]] = value
    clients = {}
    for item in manifest["clients"]:
        if set(item) != {"platform", "package"} or item["platform"] not in ("linux-amd64", "windows-amd64") or item["platform"] in clients:
            raise ValueError("invalid client platform")
        value = asset(item["package"])
        suffix = ".zip" if item["platform"] == "windows-amd64" else ".tar.gz"
        if not value["filename"].endswith(suffix) or files.get(value["filename"]) != value:
            raise ValueError("client package is not bound to signed file manifest")
        clients[item["platform"]] = value
    target = args.platform or ({"Linux": "linux-amd64", "Windows": "windows-amd64"}.get(platform.system()) if platform.machine().lower() in ("x86_64", "amd64") else None)
    if target not in clients:
        raise ValueError("release does not provide a client for this platform")
    selected = clients[target]
    if selected["size"] > 128 * 1024 * 1024:
        raise ValueError("client archive exceeds installer limit")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".phpm-install-", dir=destination.parent) as temp:
        stage = Path(temp).resolve()
        if stage.parent != destination.parent:
            raise ValueError("installation stage escaped destination parent")
        archive = stage / "client.archive"
        source.package(selected, archive)
        payload = stage / "client"
        payload.mkdir()
        extract(archive, payload, target)
        receipt = {"schema": 1, "version": manifest["version"], "commit": manifest["commit"], "root_sha256": digest(root),
                   "authorization_version": trust["version"], "package_sha256": selected["sha256"], "platform": target}
        (payload / "installation.json").write_text(json.dumps(receipt, indent=2) + "\n")
        commit_install(payload, destination)
    return {"status": "installed", "directory": str(destination), **receipt}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, help="local release directory or HTTPS directory URL")
    parser.add_argument("--root", type=Path, required=True, help="independently trusted offline root public key")
    parser.add_argument("--fingerprint", required=True, help="independently verified root SHA-256")
    parser.add_argument("--dest", type=Path, required=True, help="NEW installation directory, no PATH/profile changes")
    parser.add_argument("--version", help="expected signed client release version")
    parser.add_argument("--platform", choices=("linux-amd64", "windows-amd64"))
    parser.add_argument("--openssl", default="openssl", help="OpenSSL 3 executable")
    parser.add_argument("--ca", help="optional explicitly trusted HTTPS CA file")
    args = parser.parse_args()
    try:
        print(json.dumps(install(args)))
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, HTTPError, tarfile.TarError, zipfile.BadZipFile) as error:
        parser.exit(1, f"phpm installer: {error}\n")


if __name__ == "__main__":
    main()
