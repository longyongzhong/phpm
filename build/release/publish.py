#!/usr/bin/env python3
"""Verify a complete signed bundle, then upload its public assets to a draft only."""
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
    parser.add_argument("--expected-commit", help="require this exact signed source commit")
    parser.add_argument("--expected-version", help="require this exact signed client version")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,99}", args.tag):
        raise ValueError("invalid repository or release tag")
    result = json.loads(subprocess.check_output([str(args.verifier.resolve()), "verify", "--bundle", str(args.bundle.resolve()),
        "--root", str(args.root.resolve()), "--fingerprint", args.fingerprint], text=True))
    if (args.expected_commit and result["commit"] != args.expected_commit) or (args.expected_version and result["version"] != args.expected_version):
        raise ValueError("signed release does not match the expected source commit/version")
    repo = json.loads(gh("api", "repos/" + args.repo))
    if repo["full_name"].lower() != args.repo.lower() or type(repo.get("private")) is not bool:
        raise ValueError("repository identity/visibility could not be verified")
    commit = json.loads(gh("api", f"repos/{args.repo}/commits/{result['commit']}"))
    if commit["sha"] != result["commit"]:
        raise ValueError("signed source commit is not present in the target repository")
    manifest = json.loads((args.bundle / "clients.json").read_text())
    assets = [args.bundle / item["filename"] for item in manifest["files"]]
    assets += [args.bundle / "clients.json", args.bundle / "clients.json.sig"]
    notes = ("Disposable TEST keys; not a production trust root.\n" if args.test_preview else "Root-authorized signed candidate awaiting operator review.\n")
    notes += f"Client: {result['version']}\nSource: {result['commit']}\nRoot SHA-256: {args.fingerprint}\n"
    notes += "Individual signed assets are retained here. No private keys are included. This tool only creates drafts.\n"
    pages = json.loads(gh("api", "--paginate", "--slurp", f"repos/{args.repo}/releases?per_page=100"))
    if any(release["tag_name"] == args.tag for page in pages for release in page):
        raise ValueError("release tag already has a release; existing drafts/assets were not changed")
    with tempfile.TemporaryDirectory(prefix="phpm-release-notes-") as temporary:
        path = Path(temporary) / "request.json"
        path.write_text(json.dumps({"tag_name": args.tag, "target_commitish": result["commit"], "draft": True,
            "prerelease": True, "name": f"PHPM {result['version']}", "body": notes}), encoding="utf-8")
        created = json.loads(gh("api", f"repos/{args.repo}/releases", "--input", path))
        if not created["draft"]:
            raise ValueError("GitHub did not create the requested draft")
        print(created["html_url"])
    for start in range(0, len(assets), 20):
        gh("release", "upload", args.tag, *assets[start:start+20], "--repo", args.repo)
    # Drafts are not available through the published-release-by-tag endpoint.
    release = json.loads(gh("api", f"repos/{args.repo}/releases/{created['id']}"))
    expected = {path.name for path in assets}
    if not release["draft"] or {a["name"] for a in release["assets"]} != expected:
        raise ValueError("draft asset set does not match the signed bundle; inspect the draft")
    local = {path.name: (path.stat().st_size, "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()) for path in assets}
    for remote in release["assets"]:
        if (remote["size"], remote.get("digest")) != local[remote["name"]]:
            raise ValueError("uploaded asset size/digest mismatch; inspect the draft")
    print(json.dumps({"status": "draft-uploaded", "url": release["html_url"], "assets": len(assets), "repository_private": repo["private"]}))


if __name__ == "__main__":
    main()
