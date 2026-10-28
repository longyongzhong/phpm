# PHPM

[简体中文](README.zh-CN.md)

PHPM builds project-specific PHP CLI environments from shared PHP runtimes,
native extension artifacts and generated configuration. It does not change the
system PHP installation, global PATH or services.

This is an initial **0.1.0-dev** implementation, not a production deployment tool.

## Current scope

| Capability | Windows x64 | Linux / macOS |
|---|---|---|
| Copy/import a local PHP distribution | Implemented | Implemented; requires a relocatable, symlink-free package |
| Official PHP download + SHA-256 verification | Implemented | Not implemented |
| StaticPHP community static PHP download + published SHA-256 | Not used | Linux x64 NTS common preset; other architectures/macOS use imports |
| Import and probe local dynamic extensions | DLL | SO; requires compatible host libraries |
| `ext install`: online extension installation | Official PECL Windows x64 archives | Bundled modules; signed-catalog Redis for Debian 13 x64 |
| Project YAML, platform lock, immutable environment | Implemented | Implemented |
| Execute with independent PHP configuration | Implemented | Implemented; Unix launcher uses process replacement |
| Run Composer with project PHP; inspect declared platform requirements | Implemented; requires compatible Composer 2 PHAR for checks | Implemented; same requirement |
| Automated PHP/extension source builds or PIE integration | Not implemented | Debian 13 x64 build workflow and opt-in signed catalog; PIE not implemented |
| Signed catalog install and exact locked artifact restoration | Not used | Opt-in Debian 13 x64 NTS provider; explicit public-key trust |
| Prepare/test/activate/rollback project PHP upgrades | Implemented | Linux x64 StaticPHP downloads or preimported runtimes; compatible cached/bundled extensions |
| FPM, service restart, production deployment, garbage collection | Not implemented | Not implemented |

Native integration tests and Go vet passed on hosted Windows, macOS and Ubuntu;
the Linux bootstrap also passed its download/upgrade/rollback checks. See
[validation notes](docs/validation.md) for exact runs and remaining limitations.

The manual `Debian PHP source build (stage A)` workflow prepares PHP/Redis/SDK
archives and runs fresh-container checks before exporting unpublished, unsigned
artifacts. It does not change the CLI's Linux download source or publish Releases.
The [first successful source build](https://github.com/longyongzhong/phpm/actions/runs/40666487813)
passed SDK relocation/rebuilding and fresh-container runtime/FPM checks on Debian
13 x64. See the [source-build guide](build/linux/README.zh-CN.md) for its scope.

The opt-in signed provider adds `source add/list`, `php install --source NAME`,
matching Redis acquisition, source-preserving upgrade candidates and
`install --frozen --source NAME` for exact artifact restoration. See the
[distribution guide](docs/distribution.zh-CN.md) for key trust, offline signing,
the private draft workflow and current production limitations. Default Linux
downloads still use StaticPHP; no production signing key is bundled.

## Build

Requires Go 1.23 or newer. The only application dependency is
`gopkg.in/yaml.v3`, pinned in `go.mod`/`go.sum`.

```sh
go build -o bin/phpm ./cmd/phpm
go test ./...
go vet ./...
```

On Windows use `go build -o bin/phpm.exe ./cmd/phpm`.
Put the binary on PATH yourself, or invoke it by its full path.

## Quick start

Run these commands inside your project; `phpm` below refers to the built binary:

```sh
phpm init --php 8.4
# Windows x64: defaults to NTS; add --ts for a thread-safe build.
phpm php install 8.4
phpm install
phpm exec -- php -v
phpm exec -- php --ini
phpm verify
```

Linux x64 can use the same install commands. Its provider downloads the community
[StaticPHP common preset](https://dl.static-php.dev/v3/php-bin/common/), with
published SHA-256, static ELF/architecture checks and actual PHP identity probes.
No preinstalled PHP, Go, curl, tar or system package-manager operation is required.
The home must be writable/executable and HTTPS certificate verification must work.
This is a third-party distribution, not an official PHP Linux binary. Only builds
with published checksum files are listed/selected. NTS only; `--ts` is rejected.
It statically includes a fixed module set (including PDO MySQL, OpenSSL, mbstring
and Redis); these modules cannot have their versions switched independently, and
follow the runtime during upgrades. Linux source builds/online extension builds
remain unavailable. Host validation is reported separately in the validation notes.

On macOS/other architectures, or to choose another build, import a separately
prepared PHP distribution instead:

```sh
phpm php import /opt/my-portable-php --binary bin/php
phpm install
```

Do not import `/usr` or a directory containing PHPM_HOME. PHPM copies the whole
specified directory and rejects symlinks and special files. It does not convert
a system installation into a portable distribution or bundle its system libraries.
Do not move PHPM_HOME after generating environments; INI files contain absolute
artifact paths. Recreate environments in a new home instead.

The Windows downloader uses the official current release catalog. A minor
version selects its current patch; an exact version must match that catalog.
Historical patches are not silently substituted. Package revisions such as
`8.4.27-1` are distinguished from the binary's PHP version `8.4.27`.

## Extensions

Inside an initialized project with PHP already installed, use:

```sh
phpm ext install redis          # newest numeric release with a matching Windows archive
phpm ext install redis@6.2.0    # exact extension version; never silently substituted
phpm ext install xdebug         # Zend loader selected automatically
phpm ext install mongodb
phpm ext install curl           # use the module bundled with this PHP build
phpm ext install opcache        # bundled Zend OPcache; automatic Zend loader
phpm exec -- php --ri redis
```

`ext install` matches PHP minor version, TS/NTS, compiler and x64 architecture,
downloads from the official PHP Windows extension directory, and probes the DLL.
It preserves the existing locked PHP build and other matching locked extensions.
It then verifies the complete environment and updates **both** `phpm.yaml` and
`phpm.lock`; no separate `install` command or manual YAML edit is needed.
It requires an existing project and an installed PHP runtime.

When no version is supplied, numeric release directories are checked newest first
until one lists a matching archive. Prereleases are excluded. A matching archive
that fails to load causes an error, not a silent fallback to an older version.
Extensions unavailable in that official directory require `ext import` instead.
For bundled modules the version belongs to the selected PHP build. Compiled-in
modules such as JSON are recorded as requirements without a separate download.

The extension directory does not consistently publish checksums. First downloads
trust the official HTTPS origin, and the observed archive SHA-256 is recorded in
the cache/lock and rechecked on cache reuse. Supply `--sha256 HASH` to require an
independent expected archive digest (or the module digest for a bundled module).
Observed hashes are not publisher signatures. No remote installation scripts run.

For manual imports, obtain a trusted, compatible DLL/SO first. Imports execute PHP to test the native
module; only import native code you trust. They do not compile or download it.

```sh
phpm ext import redis@6.2.0 --file /path/to/redis.so
# Windows example:
phpm ext import redis@6.2.0 --file C:\packages\redis\php_redis.dll
# Zend extensions must use the Zend loader:
phpm ext import xdebug@3.4.0 --file /path/to/xdebug.so --zend
```

Versions above illustrate syntax; availability/compatibility depend on your
specific build. `--sha256 HASH` verifies the module against an expected checksum.
`--package-dir DIRECTORY` copies additional dependency files with the module;
the module must reside inside that directory. PHPM supplies package directories
on the child PATH for Windows DLL lookup; it does not promise isolated Unix
dynamic-library resolution. No system packages are automatically installed.

By default the target runtime comes from the current project (using its matching
lock when present). Use `--runtime ID` to select one explicitly without a project.
The probe requires the declared module name and version to match exactly, and
rejects startup warnings, missing dependencies and incompatible ABI.
Imports do not edit project configuration. Declare desired versions explicitly:

```yaml
schema: 1
php: "8.4"
extensions:
  redis: "6.2.0"
ini:
  memory_limit: "256M"
  display_errors: "0"
```

Then run `phpm install`. Separate projects can use the same PHP build and
different imported extension versions concurrently. This applies to dynamic
modules in separate processes; statically compiled extensions belong to the PHP
build and cannot be independently replaced. Extension packages requiring other
optional PHP extensions are not supported by the standalone import probe yet.
Loading order is currently alphabetical by module name, not a dependency solver.

## Prepare, test, activate and roll back PHP upgrades

An upgrade prepares a new environment before changing the project's current PHP:

```sh
phpm php upgrade                 # current PHP minor, latest build from its provider
phpm php upgrade 8.4              # explicitly choose a target branch/version
phpm diff
phpm verify --candidate
phpm exec --candidate -- php -v
phpm exec --candidate -- php path/to/project-tests.php
phpm activate CANDIDATE_ID        # use the exact ID printed by preparation
phpm history
phpm rollback                    # restore the matching previous checkpoint
```

The two `php upgrade` lines are alternatives. Preparation requires an installed,
verified current environment. It preserves TS/NTS, debug mode and the platform;
without a version argument it stays within the current minor branch. The ordinary
`exec` command continues using the old locked environment throughout preparation.
Third-party extensions retain their declared versions and are rebound to the new
PHP build; runtime-bundled/static modules follow the new PHP version. A missing or
incompatible extension aborts preparation without changing either project file.
Existing minor constraints remain minor constraints; exact PHP pins remain exact.

Review the diff and run suitable project checks with `--candidate`, then explicitly
activate the printed candidate ID. `activate` updates only this project's YAML and
lock, with a retained restore point and the existing transaction recovery mechanism.
It rechecks both environments and rejects stale candidates, corrupted artifacts or
project edits made since preparation. Tests run in the same project directory and
share application files/data; candidate execution is not a database/filesystem sandbox.

Activation and rollback affect **future launches**. Running FPM/workers/queues keep
their existing PHP until the user or service manager restarts/reloads them. Perform
application health checks after that controlled restart. No service restart, rolling
deployment or application-health assertion is performed by PHPM.

`rollback [CHECKPOINT_ID]` verifies the retained old environment and restores the
original project-file bytes. It refuses to overwrite edits made after activation.
It does not undo application code, database writes or data migrations. `history`
lists restore points (with preparation times), not deployment-completion records.
Restore points and the latest candidate are kept under PHPM_HOME/projects; do not
delete old runtimes/extensions while they are needed for running processes or rollback.

For offline use, macOS or other Linux architectures, first import the desired runtime and matching
extensions, then run `phpm php upgrade --runtime BUILD_ID`. PHP downgrades are
rejected by `upgrade`; use a retained rollback checkpoint instead. One candidate
is retained per project/home and checked against its platform. A failed preparation keeps the prior candidate;
a new successful preparation replaces it and requires its new ID for activation.

## Locking and execution

- `phpm install`: reuse the matching platform lock, or resolve installed artifacts
  when the configuration/platform entry has changed. It does not automatically
  fetch missing artifacts. Verify the environment before replacing `phpm.lock`.
- `phpm install --frozen`: require a matching existing platform/configuration lock.
- `phpm install --update`: resolve installed builds again, even with a matching lock.
- `phpm info`: print the verified environment and provenance as JSON.
- `phpm verify` / `phpm doctor`: identical initial checks for configuration,
  artifact integrity and the actual loaded PHP/extensions. Diagnostic guidance
  is currently limited to the failing check.
- `phpm exec -- php script.php`: execute with the project runtime and INI.
  The working directory is preserved. Exit status and stdio are passed through;
  Unix replaces the launcher process to preserve application signal semantics.

PATH, PHPRC and PHP_INI_SCAN_DIR are set on the child process, so ordinary nested `php`
invocations inherit the environment. An explicitly chosen absolute PHP path,
`-n`, `-c`, `-d`, or a program that rewrites its environment can override it;
PHPM is not a sandbox. Windows batch wrappers and shell builtins are not handled
by a shell automatically.

Commit `phpm.yaml` **and `phpm.lock`**. Lock entries are platform-specific and
record build IDs/digests, PHP API/build data, extension loader modes and provenance.
Local imports must be supplied independently on another machine; the lock is not
a binary distribution service or a promise of bit-for-bit source rebuilds.
Multiple builds of the same PHP patch require `runtime: <ID>` in the manifest.
Multiple unlocked extension builds with the same name/version/runtime are rejected
as ambiguous. A matching existing lock remains usable.

## Composer integration

From an installed PHPM project:

```sh
phpm composer --version
phpm ext check
phpm composer install
phpm composer require vendor/package
phpm ext check --no-dev --lock --json
```

The `composer` entry point forwards arguments, stdio and exit status using the
project PHP and configuration. `phpm exec -- composer ...` also works. Composer
continues managing PHP code dependencies and retains its usual scripts, plugins
and network behavior when explicitly invoked.

PHPM locates `composer.phar` or a PHP-format `composer` in absolute PATH
directories. Windows batch installations normally have an adjacent PHAR; the batch
launcher is bypassed so it cannot select a different PHP. Set `PHPM_COMPOSER` to
an explicit trusted PHP/PHAR file if needed. PHPM does not download Composer or
automatically execute a PHAR found in the project directory. Checks require phar
enabled in PHP and a Composer 2 PHAR that can execute on it. Deprecations from
that PHAR are summarized as a warning and preserved in the JSON `diagnostics`
field; they do not alone fail a check. Other warnings and execution errors still
fail. Update Composer or select a compatible PHAR with `PHPM_COMPOSER` as needed.

`ext check` uses Composer's constraint and platform classes to inspect declared
PHP, extension and other platform requirements. It shows missing modules, version
mismatches and requirement sources, using the real environment regardless of
`config.platform` or platform-ignore environment variables. With a Composer lock
file it includes transitive requirements; without one it checks root requirements
only and warns that transitive requirements are unknown. `--lock` requires the
lock to exist. Development requirements are included unless `--no-dev` is given.
Declared providers/replacements are honored and identified in the report.

Missing requirements, mismatches and a stale lock return nonzero. The check
respects the `COMPOSER` manifest filename, runs no project scripts, autoload files
or plugins, and leaves Composer/PHPM manifests and locks unchanged. It is not a
complete dependency solver. To install missing native extensions in one batch:

```sh
phpm ext install --missing
phpm ext install --missing --no-dev --lock
phpm composer install
```

An installed, verified PHPM environment is required. The installer intersects
root/locked requirements using Composer's own constraint matcher, then selects
the newest compatible numeric release for the current PHP build. Declared
providers/replacers already satisfying requirements are retained. OPcache uses its
Zend loader automatically. `--no-dev` excludes development requirements; `--lock`
requires a Composer lock. Without a lock, scope is explicitly limited to root
requirements. Stale locks or locks lacking a verifiable freshness hash are refused.

All acquisitions, actual PHP loading and a final Composer recheck must pass
before one transaction updates the PHPM YAML and lock. Failures retain the old
project environment; cached artifacts may remain. Composer files are unchanged,
and project scripts/plugins/autoload files are not run by the installer. A fully
satisfied environment is a no-op. Composer input edits during preparation are
detected before committing; PHPM edits use the existing transaction safeguards.

Existing extension version mismatches and PHP/system-library failures require
explicit resolution first. PHP and existing extension versions are not implicitly
upgraded. Use, for example, `phpm ext install redis@6.2.0` to explicitly change an
existing extension. Batch downloads use the existing Windows x64 provider;
Linux/macOS retain bundled-module support without automatic source builds;
the explicit signed Debian provider can also acquire its matching Redis package.
`--missing` targets the current environment only and cannot be combined with an
extension name, checksum pin or loader option.

Use `phpm ext check --candidate` or `phpm composer --candidate ...` to test a
prepared PHP upgrade before activation.

## Storage and safety

Use `PHPM_HOME` or `phpm --home DIRECTORY COMMAND`; the default is `~/.phpm`.

```text
PHPM_HOME/
  store/runtimes/<build-id>/{manifest.json,payload/}
  store/extensions/<build-id>/{manifest.json,payload/}
  environments/<environment-id>/{php.ini,conf.d/}
  projects/<project-key>/{candidate.json,history/}
  cache/
  tmp/
  locks/
```

Artifacts are copied rather than linked to a mutable source. PHPM never upgrades
them in place; the storage model is immutable by convention, not an OS permission
boundary. It verifies complete payload hashes and probes PHP before execution.
This favors correctness over startup speed in this initial release.

INI loading uses an explicit main file and a dedicated empty scan directory.
Unexpected scanned configuration, changed INI files, startup warnings or unexpected
loaded extensions fail verification. Loader settings cannot be supplied through
the project's `ini` map. INI values/paths containing newlines, NULs, quotes or `$`
are rejected rather than serialized ambiguously.

Writers use exclusive lock files and staged directories. Extension installation
uses `.phpm-transaction.json` to back up the two project files before committing.
On write failure it restores them. After a crash, execution is blocked and the next
`phpm install` or `ext install` restores the previous pair before proceeding. If
either file was edited afterward, recovery stops and preserves the backup instead
of overwriting the edit. This journal is not a substitute for version control.

A crash may leave a lock
or temporary/orphaned artifact. Confirm the owning PID is no longer running
before manually removing a stale lock; there is no automatic stale-lock recovery
or garbage collection. Network downloads use HTTPS, official-host redirects,
published SHA-256 values, size limits and traversal-safe extraction. Hashes verify
integrity against the trusted HTTPS catalog, not an independent publisher signature.

PHPM neither deploys applications nor provides Docker-equivalent security or
operating-system dependency isolation. Removing a source installation is not safe
if your imported build still links to libraries at its original location.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md), [architecture](docs/architecture.md),
[SECURITY.md](SECURITY.md) and [validation notes](docs/validation.md).

To run native isolation tests with a local PHP executable:

```sh
PHPM_TEST_PHP_BINARY=/path/to/php go test -count=1 ./...
```

Optional `PHPM_TEST_REDIS_MODULE` and `PHPM_TEST_REDIS_ALTERNATE` select two
compatible local Redis binaries with different versions. Without these, tests
still check concurrent independent INI settings on the same PHP runtime.

## License

MIT for PHPM code. Downloaded PHP, extension binaries and their libraries retain
their own licenses; PHPM does not relicense those artifacts.
