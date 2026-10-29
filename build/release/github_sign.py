#!/usr/bin/env python3
"""Sign checked same-commit materials with an Environment publishing secret, never a root private key."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile

from offline import inspect_materials, sign


def github_sign(args):
    # Remove the secret from inherited environments, including all signing tools.
    # Only this one trusted step receives it; key contents are never argv/log data.
    encoded = os.environ.pop("PHPM_RELEASE_SEED", "").strip()
    request = inspect_materials(args.materials, args.request_sha256)
    if request["commit"] != args.expected_commit or request["repository"].lower() != args.expected_repo.lower():
        raise ValueError("materials do not match the workflow source commit/repository")
    if request["version"] != args.expected_version:
        raise ValueError("materials do not match the requested release version")
    if not encoded:
        raise ValueError("configure PHPM_RELEASE_SEED in the phpm-release GitHub Environment")
    try:
        seed = base64.b64decode(encoded, validate=True)
    except ValueError:
        raise ValueError("PHPM_RELEASE_SEED must contain a base64 Ed25519 seed") from None
    if len(seed) != 32:
        raise ValueError("PHPM_RELEASE_SEED must contain a 32-byte Ed25519 seed")
    if not args.trust.is_dir():
        raise ValueError("public root authorization missing; run phpm-release setup-github and commit release/trust")
    with tempfile.TemporaryDirectory(prefix="phpm-github-key-") as temporary:
        key = Path(temporary) / "publisher.seed"
        descriptor = os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="ascii") as target:
            target.write(encoded + "\n")
        fingerprint = (args.trust / "fingerprint.txt").read_text().strip()
        result = sign(argparse.Namespace(input=args.materials, request_sha256=args.request_sha256,
            root=args.trust / "root-key.pub", fingerprint=fingerprint, trust=args.trust, key=key,
            sequence=args.sequence, validity=args.validity, output=args.output))
    result["status"] = "github-signed-and-verified"
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--materials", type=Path, required=True)
    parser.add_argument("--request-sha256", required=True)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--expected-repo", required=True)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--trust", type=Path, required=True)
    parser.add_argument("--sequence", type=int, required=True)
    parser.add_argument("--validity", default="720h")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(github_sign(args)))
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, "phpm GitHub signing: " + str(error) + "\n")


if __name__ == "__main__":
    main()
