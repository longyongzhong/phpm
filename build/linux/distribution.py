#!/usr/bin/env python3
"""Verify signed distribution using accepted stage-A packages, without rebuilding PHP."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from urllib.request import urlopen

from common import load_recipe, output, run

ROOT = Path(__file__).resolve().parent
PROJECT = ROOT.parent.parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packages", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--composer", type=Path, help="optional pinned Composer PHAR cache")
    args = parser.parse_args()
    recipe = load_recipe(ROOT)
    if output(["docker", "info", "--format", "{{.OSType}}"]) != "linux":
        raise ValueError("a running Linux Docker engine is required")
    destination = args.output.resolve()
    if args.output.is_symlink() or any(c in str(destination) for c in ",\n\r") or destination.exists():
        raise ValueError("output must be a new directory without Docker option delimiters")
    destination.parent.mkdir(parents=True, exist_ok=True)
    packages = args.packages.resolve()
    result = json.loads((packages / "build-result.json").read_text())
    with tempfile.TemporaryDirectory(prefix=".phpm-distribution-", dir=destination.parent) as temp:
        context = Path(temp)
        shutil.copyfile(ROOT / "distribution.Dockerfile", context / "Dockerfile")
        shutil.copyfile(ROOT / "runtime-packages.txt", context / "runtime-packages.txt")
        (context / "packages/verification").mkdir(parents=True)
        for name in ("build-result.json", "sdk-verification.json", "verification/runtime-verification.json"):
            shutil.copyfile(packages / name, context / "packages" / name)
        for item in result["artifacts"]:
            name = item["filename"]
            if Path(name).name != name or "/" in name or "\\" in name or ":" in name or not name.endswith(".tar.gz"):
                raise ValueError("unexpected package filename")
            shutil.copyfile(packages / name, context / "packages" / name)
        if args.composer:
            data = args.composer.read_bytes()
        else:
            with urlopen(recipe["composer"]["url"], timeout=60) as response:
                data = response.read(16 * 1024 * 1024 + 1)
        if len(data) > 16 * 1024 * 1024 or hashlib.sha256(data).hexdigest() != recipe["composer"]["sha256"]:
            raise ValueError("Composer does not match the pinned SHA-256")
        (context / "composer.phar").write_bytes(data)
        temp_go = context / "go-tmp"
        temp_go.mkdir()
        env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0", GOAMD64="v1",
                   TEMP=str(temp_go), TMP=str(temp_go), TMPDIR=str(temp_go))
        run(["go", "build", "-trimpath", "-o", context / "phpm", "./cmd/phpm"], cwd=PROJECT, env=env)
        run(["go", "test", "-c", "-o", context / "phpm.test", "./internal/phpm"], cwd=PROJECT, env=env)
        if temp_go.resolve().parent != context.resolve():
            raise ValueError("temporary Go directory escaped context")
        shutil.rmtree(temp_go)
        run(["docker", "build", "--platform", "linux/amd64", "--target", "artifacts", "--progress", "plain",
             "--build-arg", f"BASE_IMAGE={recipe['base_image']}",
             "--build-arg", f"APT_SNAPSHOT={recipe['apt_snapshot']}",
             "--output", f"type=local,dest={destination}", context], env=dict(os.environ, DOCKER_BUILDKIT="1"))
    report = json.loads((destination / "distribution-verification.json").read_text())
    if report["build_id"] != result["build_id"] or report["signing_mode"] != "ephemeral-test-key":
        raise ValueError("distribution verification report identity mismatch")
    print(f"Verified signed distribution: {destination}\nTest key only; private signing material was not exported.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"phpm distribution: {error}", file=sys.stderr)
        sys.exit(1)
