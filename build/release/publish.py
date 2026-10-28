#!/usr/bin/env python3
"""Verify a complete signed bundle, then upload its public assets to a PRIVATE draft only."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile


def gh(*args):
    return subprocess.check_output(["gh", *map(str, args)], text=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--fingerprint", required=True)
    parser.add_argument("--verifier", type=Path, required=True, help="reviewed locally built phpm-release executable")
    parser.add_argument("--test-preview", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,99}", args.tag):
        raise ValueError("invalid repository or release tag")
    result = json.loads(subprocess.check_output([str(args.verifier.resolve()), "verify", "--bundle", str(args.bundle.resolve()),
        "--root", str(args.root.resolve()), "--fingerprint", args.fingerprint], text=True))
    repo = json.loads(gh("api", "repos/" + args.repo))
    if repo["full_name"].lower() != args.repo.lower() or repo.get("private") is not True:
        raise ValueError("this rollout permits only a private repository; no visibility change is performed")
    commit = json.loads(gh("api", f"repos/{args.repo}/commits/{result['commit']}"))
    if commit["sha"] != result["commit"]:
        raise ValueError("signed source commit is not present in the target repository")
    manifest = json.loads((args.bundle / "clients.json").read_text())
    assets = [args.bundle / item["filename"] for item in manifest["files"]]
    assets += [args.bundle / "clients.json", args.bundle / "clients.json.sig"]
    notes = ("Disposable TEST keys; not a production trust root.\n" if args.test_preview else "Offline-signed candidate awaiting operator review.\n")
    notes += f"Client: {result['version']}\nSource: {result['commit']}\nRoot SHA-256: {args.fingerprint}\n"
    notes += "Individual signed assets are retained here. No private keys are included. This tool only creates drafts.\n"
    with tempfile.TemporaryDirectory(prefix="phpm-release-notes-") as temporary:
        path = Path(temporary) / "notes.txt"
        path.write_text(notes, encoding="utf-8")
        # An existing release causes gh to fail. Never replace existing assets.
        print(gh("release", "create", args.tag, "--repo", args.repo, "--target", result["commit"], "--draft", "--prerelease",
                 "--title", f"PHPM {result['version']}", "--notes-file", path).strip())
    for start in range(0, len(assets), 20):
        gh("release", "upload", args.tag, *assets[start:start+20], "--repo", args.repo)
    release = json.loads(gh("api", f"repos/{args.repo}/releases/tags/{args.tag}"))
    expected = {path.name for path in assets}
    if not release["draft"] or {a["name"] for a in release["assets"]} != expected:
        raise ValueError("draft asset set does not match the signed bundle; inspect the draft")
    local = {path.name: (path.stat().st_size, "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()) for path in assets}
    for remote in release["assets"]:
        if (remote["size"], remote.get("digest")) != local[remote["name"]]:
            raise ValueError("uploaded asset size/digest mismatch; inspect the private draft")
    print(json.dumps({"status": "private-draft-uploaded", "url": release["html_url"], "assets": len(assets)}))


if __name__ == "__main__":
    main()
