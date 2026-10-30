"""Pinned, independent shared extensions built against the selected PHP SDK."""
from pathlib import Path

from common import extract_archive, output, run

EXTENSIONS = ("redis", "xlswriter")


def configure_flags(name):
    if name == "redis":
        return ["--enable-redis", "--disable-redis-igbinary", "--disable-redis-msgpack",
                "--disable-redis-lzf", "--disable-redis-zstd", "--disable-redis-lz4"]
    if name == "xlswriter":
        return ["--with-xlswriter"]
    raise ValueError("unsupported precompiled extension")


def build_extension(recipe, name, downloads, sdk, work, env, jobs):
    version = recipe[name]["version"]
    destination = work / (name + "-source")
    extract_archive(downloads / f"{name}-{version}.tgz", destination)
    source = destination / f"{name}-{version}"
    run([sdk / "bin/phpize"], cwd=source, env=env)
    run(["./configure", f"--with-php-config={sdk / 'bin/php-config'}", *configure_flags(name)], cwd=source, env=env)
    run(["make", f"-j{jobs}"], cwd=source, env=env)
    actual = output([sdk / "bin/php", "-n", "-d", f"extension={source / 'modules' / (name + '.so')}",
                     "-r", f"echo phpversion('{name}');"])
    if actual != version:
        raise ValueError(f"{name} identity differs from the pinned recipe")
    return source
