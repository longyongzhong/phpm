#!/usr/bin/env python3
"""Rebuild Redis against an extracted, relocated development kit."""

import json
import os
from pathlib import Path
import tempfile

from common import extract_archive, output, run, sha256, write_json


def main():
    packages = Path("/out")
    result = json.loads((packages / "build-result.json").read_text())
    sdk = next(a for a in result["artifacts"] if a["kind"] == "sdk")
    if sha256(packages / sdk["filename"]) != sdk["sha256"]:
        raise ValueError("SDK archive hash mismatch")
    with tempfile.TemporaryDirectory(prefix="phpm-sdk-relocation-") as temp:
        root = Path(temp)
        relocated = root / "different-prefix"
        extract_archive(packages / sdk["filename"], relocated)
        config = relocated / "bin/php-config"
        if output([config, "--prefix"]) != str(relocated):
            raise ValueError("php-config prefix was not relocated")
        if output([config, "--include-dir"]) != str(relocated / "include/php"):
            raise ValueError("php-config headers were not relocated")
        if sha256(relocated / "bin/php") != result["php_binary_sha256"]:
            raise ValueError("SDK PHP does not match the runtime")
        run([relocated / "bin/phpize", "--version"])
        version = result["recipe"]["redis"]["version"]
        extract_archive(Path(f"/work/downloads/redis-{version}.tgz"), root / "source")
        source = root / "source" / f"redis-{version}"
        env = dict(os.environ, CFLAGS=result["cflags"], HOME=temp)
        run([relocated / "bin/phpize"], cwd=source, env=env)
        run(["./configure", f"--with-php-config={config}", "--enable-redis",
             "--disable-redis-igbinary", "--disable-redis-msgpack", "--disable-redis-lzf",
             "--disable-redis-zstd", "--disable-redis-lz4"], cwd=source, env=env)
        run(["make", "-j2"], cwd=source, env=env)
        actual = output([relocated / "bin/php", "-n", "-d", f"extension={source / 'modules/redis.so'}",
                         "-r", "echo phpversion('redis');"])
        if actual != version:
            raise ValueError("extension rebuilt with relocated SDK did not load")
        write_json(packages / "sdk-verification.json", {"schema": 1, "build_id": result["build_id"],
                   "checks": ["relocated-prefix", "relocated-headers", "php-identity", "phpize",
                              "redis-rebuild", "redis-actual-loading"]})
    print("Relocated SDK build/load verification passed.", flush=True)


if __name__ == "__main__":
    main()
