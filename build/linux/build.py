#!/usr/bin/env python3
"""Prepare a minimal Docker context; never install dependencies on the host."""

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from urllib.parse import urlparse

from common import load_recipe, output, recipe_filename, run, target_profile, verify_inputs


ROOT = Path(__file__).resolve().parent
PROJECT = ROOT.parent.parent
CONTEXT_FILES = ("Dockerfile", "recipe.json", "build-packages.txt", "runtime-packages.txt",
                 "configure-flags.txt", "common.py", "extensions.py", "runtime-packages-debian12.txt", "assemble.py", "verify-sdk.py", "verify-runtime.py")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    check = commands.add_parser("check", help="validate the recipe without Docker or network")
    inputs = commands.add_parser("verify-inputs", help="verify previously downloaded official sources")
    inputs.add_argument("directory", type=Path)
    inputs.add_argument("--gpg", default="gpg")
    inputs.add_argument("--gpgv", default="gpgv")
    build = commands.add_parser("build", help="build and validate packages using Docker BuildKit")
    build.add_argument("--output", type=Path, required=True, help="new or empty output directory")
    build.add_argument("--inputs", type=Path, help="optional verified source cache")
    build.add_argument("--jobs", type=int, default=2, choices=range(1, 33))
    for command in (check, inputs, build):
        command.add_argument("--php", choices=("8.4", "8.5"), default="8.4", help="pinned PHP recipe")
        command.add_argument("--debian", choices=("12", "13"), default="13", help="target Debian version")
    args = parser.parse_args()
    recipe_name = recipe_filename(args.php, args.debian)
    recipe = load_recipe(ROOT, recipe_name)
    for name in CONTEXT_FILES:
        if not (ROOT / name).is_file():
            raise ValueError(f"build file is missing: {name}")
    if args.command == "check":
        print(f"Recipe valid: PHP {recipe['php']['version']}, Redis {recipe['redis']['version']}, xlswriter {recipe['xlswriter']['version']}, {recipe['target']}")
        return
    if args.command == "verify-inputs":
        verify_inputs(ROOT, args.directory.resolve(), args.gpg, args.gpgv, recipe_name)
        return
    if not shutil.which("docker") or not shutil.which("go"):
        raise ValueError("the build host needs Docker BuildKit and Go >= 1.23; production hosts do not")
    if output(["docker", "info", "--format", "{{.OSType}}"]) != "linux":
        raise ValueError("a running Linux Docker engine is required")
    destination = args.output.resolve()
    if args.output.is_symlink() or any(c in str(destination) for c in ",\n\r"):
        raise ValueError("output must not be a symlink or contain Docker output-option delimiters")
    if destination.exists() and (not destination.is_dir() or any(destination.iterdir())):
        raise ValueError("output must be a new or empty directory; existing files are never removed")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".phpm-build-", dir=destination.parent) as temp:
        context = Path(temp).resolve()
        if context.parent != destination.parent:
            raise ValueError("temporary build context escaped the output parent")
        for name in CONTEXT_FILES:
            shutil.copyfile(ROOT / (recipe_name if name == "recipe.json" else name), context / name)
        shutil.copytree(ROOT / "keys", context / "keys")
        (context / "inputs").mkdir()
        if args.inputs:
            # Full signature verification is repeated inside the build container.
            for item in (recipe["php"], recipe["redis"], recipe["xlswriter"], recipe["composer"]):
                name = Path(urlparse(item["url"]).path).name
                shutil.copyfile(args.inputs / name, context / "inputs" / name)
            name = Path(urlparse(recipe["php"]["signature_url"]).path).name
            shutil.copyfile(args.inputs / name, context / "inputs" / name)
        temp_go = context / "go-tmp"
        temp_go.mkdir()
        env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0", GOAMD64="v1",
                   TEMP=str(temp_go), TMP=str(temp_go), TMPDIR=str(temp_go))
        run(["go", "build", "-trimpath", "-o", context / "phpm", "./cmd/phpm"], cwd=PROJECT, env=env)
        run(["go", "test", "-c", "-o", context / "phpm.test", "./internal/phpm"], cwd=PROJECT, env=env)
        # The daemon receives only build scripts, public keys, optional sources and the CLI.
        if temp_go.resolve().parent != context:
            raise ValueError("Go temporary directory escaped the owned build context")
        shutil.rmtree(temp_go)
        run(["docker", "build", "--platform", "linux/amd64", "--target", "artifacts", "--progress", "plain",
             "--build-arg", f"BASE_IMAGE={recipe['base_image']}",
             "--build-arg", f"APT_SNAPSHOT={recipe['apt_snapshot']}",
             "--build-arg", f"DEBIAN_SUITE={target_profile(recipe)[1]}",
             "--build-arg", f"BUILD_JOBS={args.jobs}",
             "--output", f"type=local,dest={destination}", context], env=dict(os.environ, DOCKER_BUILDKIT="1"))
    report = json.loads((destination / "verification/runtime-verification.json").read_text())
    if report.get("build_id") is None:
        raise ValueError("build returned without a successful runtime verification report")
    print(f"Verified stage-A packages: {destination}\nNo Release was published; packages are unsigned.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"phpm source build: {error}", file=sys.stderr)
        sys.exit(1)
