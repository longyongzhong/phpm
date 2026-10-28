"""Standard-library helpers for the experimental Debian source-build pipeline."""

import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import tempfile
from urllib.parse import urlparse


def run(args, **kwargs):
    print("+", " ".join(map(str, args)), flush=True)
    return subprocess.run(list(map(str, args)), check=True, **kwargs)


def output(args, **kwargs):
    return subprocess.check_output(list(map(str, args)), text=True, **kwargs).strip()


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def read_lines(path):
    return [line.strip() for line in Path(path).read_text().splitlines()
            if line.strip() and not line.lstrip().startswith("#")]


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def load_recipe(root):
    recipe = json.loads((root / "recipe.json").read_text(encoding="utf-8"))
    if recipe["schema"] != 1 or recipe["target"] != "debian13-x86_64-nts":
        raise ValueError("unsupported recipe schema/target")
    if not re.fullmatch(r"debian:13-slim@sha256:[a-f0-9]{64}", recipe["base_image"]):
        raise ValueError("a pinned official Debian 13 image is required")
    if not re.fullmatch(r"\d{8}T\d{6}Z", recipe["apt_snapshot"]):
        raise ValueError("invalid Debian snapshot timestamp")
    if type(recipe["build_revision"]) is not int or recipe["build_revision"] < 1:
        raise ValueError("invalid build revision")
    if type(recipe["source_date_epoch"]) is not int or recipe["source_date_epoch"] < 1:
        raise ValueError("invalid source date epoch")
    for name, host in (("php", "www.php.net"), ("redis", "pecl.php.net"), ("composer", "getcomposer.org")):
        item = recipe[name]
        if not re.fullmatch(r"\d+\.\d+\.\d+", item["version"]):
            raise ValueError("source versions must be exact numeric releases")
        if not re.fullmatch(r"[a-f0-9]{64}", item["sha256"]):
            raise ValueError("source requires a pinned SHA-256")
        url = urlparse(item["url"])
        expected = {"php": f"/distributions/php-{item['version']}.tar.xz",
                    "redis": f"/get/redis-{item['version']}.tgz",
                    "composer": f"/download/{item['version']}/composer.phar"}[name]
        if url.scheme != "https" or url.netloc != host or url.path != expected or url.query or url.fragment:
            raise ValueError("source must use its fixed official HTTPS release URL")
    php = recipe["php"]
    if not php["version"].startswith("8.4.") or php["signature_url"] != php["url"] + ".asc":
        raise ValueError("this recipe requires PHP 8.4 and its detached release signature")
    if not re.fullmatch(r"[A-F0-9]{40}", php["signer"]) or php["key"] != "keys/php-8.4.asc":
        raise ValueError("invalid PHP release key pin")
    if not (root / php["key"]).is_file():
        raise ValueError("vendored PHP release key is missing")
    for filename in ("build-packages.txt", "runtime-packages.txt"):
        packages = read_lines(root / filename)
        if not packages or len(packages) != len(set(packages)) or any(
                not re.fullmatch(r"[a-z0-9][a-z0-9+.-]*", p) for p in packages):
            raise ValueError("invalid or duplicate Debian package name")
    flags = read_lines(root / "configure-flags.txt")
    if "--prefix=/opt/phpm-build-prefix" not in flags or any(
            not flag.startswith("--") or any(c.isspace() for c in flag) for flag in flags):
        raise ValueError("invalid configure flags")
    if any(flag.startswith(("--enable-zts", "--enable-debug")) for flag in flags):
        raise ValueError("the NTS release profile cannot enable ZTS or debug")
    return recipe


def verify_inputs(root, directory, gpg="gpg", gpgv="gpgv"):
    recipe = load_recipe(root)
    for name in ("php", "redis", "composer"):
        item = recipe[name]
        path = directory / Path(urlparse(item["url"]).path).name
        if not path.is_file() or sha256(path) != item["sha256"]:
            raise ValueError(f"missing or modified {name} input: {path}")
    php = recipe["php"]
    archive = directory / Path(urlparse(php["url"]).path).name
    with tempfile.TemporaryDirectory(prefix="phpm-gpg-") as temp:
        keyring = Path(temp) / "release.gpg"
        run([gpg, "--no-options", "--batch", "--homedir", Path(temp).as_posix(),
             "--dearmor", "--output", keyring.as_posix(), (root / php["key"]).as_posix()])
        result = subprocess.run([gpgv, "--homedir", Path(temp).as_posix(), "--keyring", keyring.as_posix(),
                                 "--status-fd", "1", str(archive) + ".asc", str(archive)],
                                text=True, capture_output=True, check=True)
        valid = [line.split()[2] for line in result.stdout.splitlines()
                 if line.startswith("[GNUPG:] VALIDSIG ")]
        if valid != [php["signer"]]:
            raise ValueError("PHP signature does not match the pinned release signer")
    print("Pinned source hashes and PHP release signature verified.", flush=True)
    return recipe


def extract_archive(archive, destination, max_bytes=1024 * 1024 * 1024):
    """Extract only bounded regular files/directories; never follow tar links."""
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=False)
    total = 0
    count = 0
    with tarfile.open(archive, "r:*") as source:
        for entry in source:
            count += 1
            path = PurePosixPath(entry.name)
            if (path.is_absolute() or ".." in path.parts or "\\" in entry.name or ":" in entry.name
                    or any(ord(c) < 32 for c in entry.name) or not path.parts):
                raise ValueError("unsafe archive path")
            if not (entry.isfile() or entry.isdir()):
                raise ValueError("archive contains a link or special file")
            total += entry.size
            if total > max_bytes or count > 50000 or entry.size < 0:
                raise ValueError("archive exceeds extraction limits")
            target = destination.joinpath(*path.parts)
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            with target.open("xb") as out, source.extractfile(entry) as data:
                remaining = entry.size
                while remaining:
                    chunk = data.read(min(remaining, 1024 * 1024))
                    if not chunk:
                        raise ValueError("truncated archive member")
                    out.write(chunk)
                    remaining -= len(chunk)
            target.chmod(0o755 if entry.mode & 0o111 else 0o644)
            if not 0 <= entry.mtime <= 4102444800:
                raise ValueError("archive timestamp is outside the supported range")
            # Preserve generated-header/source ordering; otherwise make can try
            # to regenerate upstream files and fetch unpinned build tools.
            os.utime(target, (entry.mtime, entry.mtime))


def pack_archive(root, archive, epoch):
    """Stable ordering/ownership/timestamps; this alone does not prove bit reproducibility."""
    with open(archive, "xb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as zipped:
        with tarfile.open(fileobj=zipped, mode="w") as target:
            for path in sorted(Path(root).rglob("*")):
                if path.is_symlink() or not (path.is_file() or path.is_dir()):
                    raise ValueError("packages must contain only regular files/directories")
                info = target.gettarinfo(str(path), path.relative_to(root).as_posix())
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                info.mtime = epoch
                info.mode = 0o755 if path.is_dir() or info.mode & 0o111 else 0o644
                if path.is_file():
                    with path.open("rb") as data:
                        target.addfile(info, data)
                else:
                    target.addfile(info)


def relocate_sdk_script(text):
    prefix = "/opt/phpm-build-prefix"
    pattern = r"(?m)^prefix=['\"]" + re.escape(prefix) + r"['\"]$"
    replacement = 'prefix="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"'
    text, count = re.subn(pattern, lambda _: replacement, text)
    if count != 1:
        raise ValueError("unexpected upstream SDK script prefix declaration")
    return text.replace(prefix, "${prefix}")
