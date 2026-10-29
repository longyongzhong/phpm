#!/usr/bin/env python3
"""Exercise shipped offline signing with discarded TEST keys and a real client; no formal keys."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from offline import inspect_materials, maintainer_tool


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--materials", type=Path, required=True)
    parser.add_argument("--signer", choices=("offline", "github"), default="offline")
    args = parser.parse_args()
    materials = args.materials.resolve()
    digest = hashlib.sha256((materials / "release-inputs.json").read_bytes()).hexdigest()
    request = inspect_materials(materials, digest)
    tool = maintainer_tool(materials)
    with tempfile.TemporaryDirectory(prefix="phpm-offline-roundtrip-") as temporary:
        work = Path(temporary)
        for name in ("root", "publisher"):
            subprocess.run([str(tool), "keygen", "--private", str(work / (name + ".seed")), "--public", str(work / (name + ".pub"))], check=True)
        fingerprint = hashlib.sha256(base64.b64decode((work / "root.pub").read_bytes().strip())).hexdigest()
        subprocess.run([str(tool), "trust", "--root-key", str(work / "root.seed"), "--key", str(work / "publisher.pub"),
                        "--version", "1", "--validity", "72h", "--output", str(work / "trust")], check=True)
        if args.signer == "github":
            (work / "trust/fingerprint.txt").write_text(fingerprint + "\n")
            subprocess.run([sys.executable, str(Path(__file__).with_name("github_sign.py")), "--materials", str(materials),
                "--request-sha256", digest, "--expected-commit", request["commit"], "--expected-repo", request["repository"],
                "--expected-version", request["version"], "--trust", str(work / "trust"), "--sequence", "1", "--validity", "1h",
                "--output", str(work / "signed")], check=True,
                env=dict(os.environ, PHPM_RELEASE_SEED=(work / "publisher.seed").read_text().strip()))
        else:
            subprocess.run([sys.executable, str(materials / "offline-sign.py"), "sign", "--input", str(materials),
                "--request-sha256", digest, "--root", str(work / "root.pub"), "--fingerprint", fingerprint,
                "--trust", str(work / "trust"), "--key", str(work / "publisher.seed"), "--sequence", "1", "--validity", "1h",
                "--output", str(work / "signed")], check=True)
        subprocess.run([sys.executable, str(materials / "install-phpm.py"), "--source", str(work / "signed"),
            "--root", str(work / "root.pub"), "--fingerprint", fingerprint, "--dest", str(work / "installed"),
            "--version", request["version"]], check=True)
        binary = work / "installed" / ("phpm.exe" if os.name == "nt" else "phpm")
        version = subprocess.check_output([str(binary), "version"], text=True).strip()
        if version != request["version"]:
            raise ValueError("installed client does not report the signed release version")
        print(json.dumps({"status": args.signer + "-roundtrip-passed", "version": version, "formal_keys_used": False}))


if __name__ == "__main__":
    main()
