#!/usr/bin/env python3
"""Build unsigned stage-A packages inside the pinned Debian container only."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
from urllib.parse import urlparse

from common import (extract_archive, load_recipe, output, pack_archive, read_lines,
                    relocate_sdk_script, run, sha256, verify_inputs, write_json)


ROOT = Path("/recipe")
WORK = Path("/work")
OUT = Path("/out")
PREFIX = "/opt/phpm-build-prefix"


def fetch(item, downloads, signature=False):
    url = item["signature_url"] if signature else item["url"]
    dest = downloads / Path(urlparse(url).path).name
    if dest.exists():
        if not signature and sha256(dest) != item["sha256"]:
            raise ValueError(f"cached source is corrupted: {dest}")
        return
    part = dest.with_name(dest.name + ".part")
    run(["curl", "--fail", "--location", "--proto", "=https", "--proto-redir", "=https",
         "--retry", "3", "--connect-timeout", "20", "--max-time", "600", "--max-filesize",
         "536870912", "--output", part, url])
    if not signature and sha256(part) != item["sha256"]:
        raise ValueError(f"official source SHA-256 mismatch: {url}")
    part.rename(dest)


def copy_file(source, target):
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)


def copy_licenses(source, target):
    for path in source.rglob("*"):
        if path.is_file() and path.name.upper().startswith(("LICENSE", "COPYING", "NOTICE")):
            copy_file(path, target / path.relative_to(source))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fetch-only", action="store_true")
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise SystemExit("assemble.py requires the recipe's Linux x86_64 container")
    recipe = load_recipe(ROOT)
    if 'VERSION_ID="13"' not in Path("/etc/os-release").read_text():
        raise SystemExit("Debian 13 is required")
    if Path(PREFIX).exists():
        raise SystemExit("build prefix must not exist; relocation tests require a staged install")
    downloads = WORK / "downloads"
    downloads.mkdir(exist_ok=True)
    for source in (ROOT / "inputs").glob("*"):
        if not source.is_file() or source.is_symlink():
            raise ValueError("input cache must contain regular files only")
        shutil.copyfile(source, downloads / source.name)
    for name in ("php", "redis", "composer"):
        fetch(recipe[name], downloads)
    fetch(recipe["php"], downloads, signature=True)
    verify_inputs(ROOT, downloads)
    if args.fetch_only:
        return

    env = dict(os.environ, SOURCE_DATE_EPOCH=str(recipe["source_date_epoch"]),
               CFLAGS="-O2 -g0 -march=x86-64 -mtune=generic -ffile-prefix-map=/work=/usr/src/phpm",
               CXXFLAGS="-O2 -g0 -march=x86-64 -mtune=generic -ffile-prefix-map=/work=/usr/src/phpm",
               LC_ALL="C.UTF-8", TZ="UTC", HOME=str(WORK))
    jobs = int(os.environ.get("PHPM_BUILD_JOBS", "2"))
    if not 1 <= jobs <= 32:
        raise ValueError("build jobs must be between 1 and 32")
    extract_archive(downloads / f"php-{recipe['php']['version']}.tar.xz", WORK / "php-source")
    php_source = WORK / "php-source" / f"php-{recipe['php']['version']}"
    flags = read_lines(ROOT / "configure-flags.txt")
    # PHP 8.5 makes OPcache part of the binary; the old configure switch and
    # a separate opcache.so are no longer applicable to that upstream series.
    if recipe["php"]["version"].startswith("8.5."):
        flags.remove("--enable-opcache")
    run(["./configure", *flags], cwd=php_source, env=env)
    run(["make", f"-j{jobs}"], cwd=php_source, env=env)
    run(["make", "test", "TESTS=tests/basic ext/json/tests ext/pdo_sqlite/tests",
         "NO_INTERACTION=1", "REPORT_EXIT_STATUS=1"], cwd=php_source, env=env)
    stage = WORK / "stage"
    run(["make", "install", f"INSTALL_ROOT={stage}"], cwd=php_source, env=env)
    installed = stage / PREFIX.lstrip("/")
    runtime = WORK / "runtime"
    sdk = WORK / "sdk"
    copy_file(installed / "bin/php", runtime / "bin/php")
    copy_file(installed / "sbin/php-fpm", runtime / "sbin/php-fpm")
    for module in sorted((installed / "lib/php/extensions").rglob("*.so")):
        copy_file(module, runtime / "ext" / module.name)
    expected = {"bcmath", "curl", "exif", "fileinfo", "gd", "mbstring", "opcache", "pdo_mysql", "sockets", "zip"}
    if recipe["php"]["version"].startswith("8.5."):
        expected.remove("opcache")
        if output([runtime / "bin/php", "-n", "-r", "echo extension_loaded('Zend OPcache') ? 'yes' : 'no';"]) != "yes":
            raise ValueError("PHP 8.5 must provide built-in OPcache")
    if {p.stem for p in (runtime / "ext").glob("*.so")} != expected:
        raise ValueError("built shared module set differs from the recipe")
    copy_file(installed / "bin/php", sdk / "bin/php")
    for name in ("phpize", "php-config"):
        script = sdk / "bin" / name
        script.write_text(relocate_sdk_script((installed / "bin" / name).read_text()), encoding="utf-8")
        script.chmod(0o755)
    shutil.copytree(installed / "include", sdk / "include")
    shutil.copytree(installed / "lib/php/build", sdk / "lib/php/build")
    copy_licenses(php_source, runtime / "licenses/php")
    copy_licenses(php_source, sdk / "licenses/php")

    extract_archive(downloads / f"redis-{recipe['redis']['version']}.tgz", WORK / "redis-source")
    redis_source = WORK / "redis-source" / f"redis-{recipe['redis']['version']}"
    run([sdk / "bin/phpize"], cwd=redis_source, env=env)
    run(["./configure", f"--with-php-config={sdk / 'bin/php-config'}", "--enable-redis",
         "--disable-redis-igbinary", "--disable-redis-msgpack", "--disable-redis-lzf",
         "--disable-redis-zstd", "--disable-redis-lz4"], cwd=redis_source, env=env)
    run(["make", f"-j{jobs}"], cwd=redis_source, env=env)
    redis = WORK / "redis"
    copy_file(redis_source / "modules/redis.so", redis / "redis.so")
    copy_licenses(redis_source, redis / "licenses/redis")
    version = output([runtime / "bin/php", "-n", "-d", f"extension={redis / 'redis.so'}",
                      "-r", "echo phpversion('redis');"])
    if version != recipe["redis"]["version"]:
        raise ValueError("Redis identity probe did not match the recipe")

    recipe_files = sorted(p for p in ROOT.rglob("*") if p.is_file()
                          and p.suffix != ".pyc" and "__pycache__" not in p.parts
                          and "inputs" not in p.relative_to(ROOT).parts and p.name != "phpm")
    recipe_hashes = {p.relative_to(ROOT).as_posix(): sha256(p) for p in recipe_files}
    recipe_digest = hashlib.sha256(json.dumps(recipe_hashes, sort_keys=True).encode()).hexdigest()
    build_id = f"php-{recipe['php']['version']}-{recipe['target']}-b{recipe['build_revision']}-{recipe_digest[:12]}"
    packages = output(["dpkg-query", "-W", "-f=${Package}\t${Version}\n"]).splitlines()
    php_info = output([runtime / "bin/php", "-n", "-i"])
    api = {}
    for label in ("PHP API", "PHP Extension Build", "Zend Extension Build"):
        match = re.search(r"(?m)^" + re.escape(label) + r" => (.+)$", php_info)
        if not match:
            raise ValueError(f"PHP did not report {label}")
        api[label] = match.group(1)
    base = {"schema": 1, "build_id": build_id, "target": recipe["target"], "php_version": recipe["php"]["version"],
            "release_status": "unsigned-stage-a", "recipe": recipe, "recipe_files": recipe_hashes,
            "compiler": output(["cc", "--version"]).splitlines()[0], "cflags": env["CFLAGS"],
            "configure": flags, "php_api": api, "glibc": output(["getconf", "GNU_LIBC_VERSION"]),
            "build_packages": packages, "runtime_packages": read_lines(ROOT / "runtime-packages.txt"),
            "php_binary_sha256": sha256(runtime / "bin/php")}
    artifacts = []
    for kind, payload in (("runtime", runtime), ("sdk", sdk), ("redis", redis)):
        receipt = dict(base, kind=kind)
        if kind == "redis":
            receipt.update(extension="redis", extension_version=version, runtime_build=build_id)
        write_json(payload / "phpm-build.json", receipt)
        filename = f"{build_id}-{kind}.tar.gz"
        pack_archive(payload, OUT / filename, recipe["source_date_epoch"])
        artifacts.append({"kind": kind, "filename": filename, "sha256": sha256(OUT / filename),
                          "size": (OUT / filename).stat().st_size})
    write_json(OUT / "build-result.json", dict(base, artifacts=artifacts))
    (OUT / "SHA256SUMS").write_text("".join(f"{a['sha256']}  {a['filename']}\n" for a in artifacts)
                                    + f"{sha256(OUT / 'build-result.json')}  build-result.json\n", encoding="utf-8")
    print(f"Built {build_id}; runtime validation still required.", flush=True)


if __name__ == "__main__":
    main()
