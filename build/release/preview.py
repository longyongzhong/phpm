#!/usr/bin/env python3
"""Create a short-lived release rehearsal with disposable keys, never production keys."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

PROJECT = Path(__file__).resolve().parents[2]


def run(args, **kwargs):
    subprocess.run(list(map(str, args)), check=True, cwd=PROJECT, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--target", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True, help="NEW directory for release assets")
    args = parser.parse_args()
    output = args.output.resolve()
    if output.exists():
        raise ValueError("preview destination already exists")
    output.parent.mkdir(parents=True, exist_ok=True)
    reports = [json.loads((directory / "build-result.json").read_text()) for directory in (args.base, args.target)]
    if [report["php_version"] for report in reports] != ["8.4.27", "8.5.12"]:
        raise ValueError("rehearsal requires accepted PHP 8.4.27 and 8.5.12 packages in that order")
    with tempfile.TemporaryDirectory(prefix=".phpm-release-test-", dir=output.parent) as temporary:
        work = Path(temporary)
        env = dict(os.environ, CGO_ENABLED="0", GOAMD64="v1")
        tool = work / ("phpm-release.exe" if os.name == "nt" else "phpm-release")
        run(["go", "build", "-trimpath", "-o", tool, "./cmd/phpm-release"], env=env)
        binaries = {}
        for target in ("linux", "windows"):
            binary = work / ("phpm.exe" if target == "windows" else "phpm")
            run(["go", "build", "-trimpath", "-o", binary, "./cmd/phpm"], env=dict(env, GOOS=target, GOARCH="amd64"))
            binaries[target] = binary
        for name in ("root", "publisher"):
            run([tool, "keygen", "--private", work / (name + ".seed"), "--public", work / (name + ".pub")])
        run([tool, "trust", "--root-key", work / "root.seed", "--version", "1", "--key", work / "publisher.pub",
             "--output", work / "trust", "--validity", "72h"])
        (work / "root.seed").unlink()
        run([tool, "catalog", "--key", work / "publisher.seed", "--root", work / "root.pub", "--trust", work / "trust",
             "--sequence", "1", "--validity", "48h", "--output", work / "php", args.base.resolve(), args.target.resolve()])
        commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=PROJECT, text=True).strip()
        version = "0.1.0-preview." + commit[:12]
        run([tool, "clients", "--bundle", work / "php", "--root", work / "root.pub", "--key", work / "publisher.seed",
             "--version", version, "--commit", commit, "--linux", binaries["linux"], "--windows", binaries["windows"],
             "--notices", PROJECT, "--installer", PROJECT / "scripts/install-phpm.py", "--output", output, "--validity", "48h"])
        (work / "publisher.seed").unlink()
        fingerprint = hashlib.sha256(base64.b64decode((work / "root.pub").read_bytes().strip())).hexdigest()
        run([tool, "verify", "--bundle", output, "--root", work / "root.pub", "--fingerprint", fingerprint])
        # Use the independently implemented verifier on the real current-platform
        # archive; the executable is started only by this acceptance harness.
        installed = work / "installed"
        run([os.sys.executable, PROJECT / "scripts/install-phpm.py", "--source", output, "--root", work / "root.pub",
             "--fingerprint", fingerprint, "--dest", installed, "--version", version])
        run([installed / ("phpm.exe" if os.name == "nt" else "phpm"), "version"])
        print(json.dumps({"status": "verified-test-preview", "version": version, "commit": commit,
                          "root_sha256": fingerprint, "production_keys_used": False}))


if __name__ == "__main__":
    main()
