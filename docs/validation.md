# Local validation

Date: 2026-10-27 (Asia/Shanghai).

Host: Windows x64; Go 1.23.2. The workspace initially contained no project files
and was not a Git repository. No repository, commit, service, system dependency
or global PATH change was created as part of validation.

## Executed successfully

- `go test -count=1 -cover ./...` with native PHP, two Redis module paths and the
  compiled CLI enabled. Core package coverage was 57.9% after the online installer follow-up;
  subprocess CLI execution is not included in the parent test process coverage.
- `go vet ./...`.
- Native Windows executable build.
- Linux amd64, macOS amd64 and macOS arm64 cross-compilation.
- Live official Windows PHP download, published catalog SHA-256 verification,
  ZIP extraction, runtime probing, import and final-location verification.
- Full CLI smoke flow: `init`, `php list`, `ext import`, `install`, `verify`,
  `exec -- php`, with application exit code 23 preserved.
- Nested bare `php` launched from PHP inherited the selected PHP version and INI.
- The PHP executable already on the host PATH still reported PHP 7.4.33 afterward.

## Concrete dual-project experiment

The live downloaded build reported PHP **8.4.27**, Windows x64, NTS. Its official
package version was **8.4.27-1**. Both project processes used the same PHP binary:

| Project | PHP | Redis module | Result |
|---|---|---|---|
| A | 8.4.27 NTS | 6.1.0, PHP 8.4 NTS VS17 x64 | Loaded expected version |
| B | 8.4.27 NTS | 6.2.0, PHP 8.4 NTS VS17 x64 | Loaded expected version |

The integration test starts both processes before waiting, checks the binary path,
Redis versions and different memory limits, and verifies no external INI leaked
into either environment. Separate real CLI projects were also created under the
ignored `.cache/cli-projects/` directory and executed against the same stored build.
The integration test also passed using the existing PHP 8.2.9 and Redis 6.1.0
from the local installation (copied into an isolated test fixture).

Artifacts used for the PHP 8.4 test came from official PHP HTTPS locations:

- [PHP catalog](https://downloads.php.net/~windows/releases/releases.json)
- [Redis 6.1.0 Windows packages](https://downloads.php.net/~windows/pecl/releases/redis/6.1.0/)
- [Redis 6.2.0 Windows packages](https://downloads.php.net/~windows/pecl/releases/redis/6.2.0/)

The extension archive digests below were calculated locally for traceability;
they are not independently published publisher signatures:

```text
php_redis-6.1.0-8.4-nts-vs17-x64.zip
5a8c7d8ce9a653370dfa635102ee3723273c138a9677d507406ddf4f04aecfd6

php_redis-6.2.0-8.4-nts-vs17-x64.zip
314b78cab38f47a68ea116a7acae28271ec0d9855b4ecb7d8706db8d51771490
```

## Failure cases exercised

- Unknown/duplicate YAML keys, multiple documents, invalid versions and protected
  loader settings fail without silently correcting the configuration.
- ZIP traversal using either slash convention, rooted/drive paths and duplicate
  file entries are rejected.
- Download checksum mismatch is not committed; corrupted cached files are rejected.
- Concurrent writers cannot acquire the same exclusive lock.
- Runtime selection compares numeric patch versions and rejects ambiguous builds.
- Locked builds do not float when a newer local patch appears.
- Changed runtime payloads fail integrity verification.
- Missing extensions fail installation without replacing the existing project lock;
  the other project's environment remains usable.
- External INI scanning does not leak into the environment; added environment INI
  files fail verification.
- Copying a directory into its own subtree is rejected.
- Existing source fixture files remain unchanged after imports and execution.
- Empty optional YAML maps normalize identically to omitted maps for lock round trips.

## Environmental findings

The default Go proxy was unreachable from this host; the fixed YAML dependency was
obtained using goproxy.cn with normal Go checksum verification. Go build/module
caches were confined to `.cache/`.

The restricted host's redirected system temporary directory did not allow rename
operations. Tests were rerun with TEMP/TMP under workspace `.cache/tmp`, preserving
the file replacement assertions. Unit HTTP tests use an in-memory transport and
do not require loopback sockets. The real PHP downloader was tested separately
against the official server.

## Not established by these checks

- Linux/macOS native behavior: only cross-compilation was performed locally.
- The GitHub Actions workflow: configured for Windows/Linux/macOS, not run here.
- Extension dependency graphs or complex external-library packages.
- Windows TS builds, ARM64 runtimes, FPM or production deployments.
- Extension source builds, PIE integration or Linux/macOS PHP builds.
- Bit-for-bit source rebuilds or exact host system-library reproduction.
- Power-loss durability, network-filesystem atomicity or automatic stale writer-lock recovery.

## Online extension installation follow-up

The following live `ext install` commands completed on the same Windows PHP
8.4.27 NTS x64 build, in an isolated `.cache/online-extension-project`:

| Request | Resolved result | Source |
|---|---|---|
| `redis@6.2.0` | 6.2.0 | Official Windows PECL archive |
| `redis` | 6.3.0 | Newest matching numeric release directory |
| `xdebug` | 3.5.3 | Official archive; Zend loader selected automatically |
| `mongodb` | 2.5.5 | Official Windows PECL archive |
| `curl` | 8.4.27 | Runtime-bundled DLL |
| `opcache` | 8.4.27 | Runtime-bundled DLL; Zend loader and module-name mapping |
| `json` | 8.4.27 | Compiled-in requirement; no download |

The final environment passed `verify` and a real PHP process reported all the
versions above. Each successful command updated the project YAML and lock without
manual edits or a separate install step. The test used the same store that already
contained manual Redis imports, exercising explicit selection amid multiple builds.

New automated tests cover exact TS/NTS/compiler/architecture archive selection,
numeric sorting and prerelease exclusion, exact-version refusal, optional checksum
pins, observed-hash cache reuse and corruption, failed HTTP downloads, preservation
of the locked PHP on acquisition failure, YAML comments/aliases, two-file commit
failure rollback, interrupted transactions and refusal to overwrite editor changes.
Native tests also exercise compiled-in extension installation through the CLI.

A final live checksum-pin test explicitly selected Redis 6.2.0 using its expected
archive SHA-256, retaining Xdebug/MongoDB and the bundled modules. A subsequent
all-zero checksum was rejected; byte hashes of both project files were unchanged
and the previously installed environment still passed verification.

Transaction recovery is tested for process interruption states; power-loss durability
and automatic stale writer-lock reclamation remain outside the verified guarantees.

## Staged PHP upgrade follow-up

A live Windows test project imported a separate PHP 8.2.9 NTS x64 distribution and
declared Redis 6.1.0, curl, OPcache, JSON and memory_limit=256M. `php upgrade 8.2`
downloaded the official PHP 8.2.35 build and prepared the complete candidate:

- Ordinary `exec` continued reporting PHP 8.2.9 after preparation.
- Both project-file hashes stayed unchanged throughout preparation.
- Candidate execution reported PHP 8.2.35, Redis 6.1.0, curl/OPcache 8.2.35 and
  the original memory limit. Third-party Redis was not upgraded independently.
- `activate` with the reviewed candidate ID selected PHP 8.2.35 for future launches.
- `history` retained the restore point; `rollback` restored PHP 8.2.9 and both
  project files byte for byte.
- The PHP executable on the host PATH still reported PHP 7.4.33 afterward.

The opt-in native integration fixture also models a same-version package revision
using two distinct immutable PHP payloads. It checks preparation without switching,
explicit candidate IDs, failed acquisition retaining an earlier candidate, cached
extension rebinding, corrupted target rejection, stale-editor guards, activation,
exact rollback and an already-running old PHP process remaining on its old binary.
No application service or production project was restarted or switched in these tests.

## Composer integration follow-up

Windows native checks used the isolated PHP 8.4.27 NTS x64 build and official
Composer 2.10.3 PHAR. The PHAR was downloaded into `.cache/composer-current/`
and verified against the published SHA-256
`7a2d379d5b8ffdaa028580ef26494c36d2feef4b178d3dd1473a4dbc5e17c8d6`.
The host's Composer 2.7.1 installation was left intact; its PHP 8.4 deprecation
diagnostics prompted use of the separate compatible PHAR for these checks.

- `composer --version` and a real Composer `@php` script reported the managed
  PHP 8.4.27 executable. Bundled OpenSSL was enabled only in the smoke project
  to run the normal Composer command.
- A root-only check identified missing Redis/Xdebug, excluded Xdebug with
  `--no-dev`, ignored a simulated Redis `config.platform` version, and explained
  that a missing lock prevents transitive inspection.
- Native automated fixtures exercised locked transitive requirements, production
  versus development scope, declared polyfill providers, version mismatches,
  stale locks, invalid constraints and mandatory-lock failure. Project scripts
  and autoload marker files were not executed. Composer/PHPM manifests and locks
  remained byte-identical across read-only checks.
- Subprocess fixtures verified both Composer entry points, arguments containing
  spaces, selected PHP binary/version and exit status 23.
- A live project requiring PHP >=8.2.30 failed against current PHP 8.2.9 and
  passed against its prepared 8.2.35 candidate. Both project-file hashes were
  unchanged; the host PATH PHP still reported 7.4.33.

The native Go suite and `go vet` passed locally, including the Composer subtest.
The embedded PHP adapter passed PHP syntax validation. Updated Linux amd64 and
macOS amd64/arm64 binaries were cross-compiled; native validation on those systems
and execution of the updated CI workflow remain unperformed. Composer checks in
native fixtures explicitly skip if the imported test PHP lacks compiled-in phar
or no Composer PHP payload is available. Tests do not install application packages.

### Older Composer deprecation compatibility fix

Reproduced the reported Composer 2.7.1/PHP 8.4.27 diagnostics using the unchanged
host PHAR at `D:\programs\composer\composer.phar`. The checker now records only
deprecations originating inside the selected PHAR as structured JSON diagnostics
and a human-readable warning. It does not lower PHP error reporting or discard
other warnings/errors. This supersedes the earlier behavior described above that
rejected the older PHAR for emitting deprecations.

The old PHAR now completes a satisfied PHP/JSON check with exit status 0 and four
recorded deprecations; a missing Redis requirement still returns status 1.
Project YAML and lock hashes remain unchanged. Native regression fixtures copy
the trusted Composer PHAR into temporary archives and inject a deprecation,
warning or exception: deprecations retain valid requirements/results, while the
warning and exception retain the injected failure messages. The Windows native
suite and Go vet passed with Composer 2.7.1 and 2.10.3. Global Composer was not updated.

## Missing-extension batch installation (2026-10-28)

The live Windows fixture `.cache/missing-extension-smoke-project` started with a
verified PHP 8.4.27 environment and no optional modules. Its Composer metadata
fixture required curl, Zend OPcache and Redis ^6.0; a locked fixture dependency
also required Redis <6.2. `ext install --missing --no-dev --lock` completed using
the unchanged Composer 2.7.1 PHAR and official Windows extension provider:

- Enabled runtime-bundled curl/OPcache 8.4.27 and downloaded Redis 6.1.0 for
  PHP 8.4 NTS VS17 x64, honoring both root and transitive constraints.
- Excluded the root development Xdebug requirement with `--no-dev`.
- Passed a real PHP loading check and `ext check --no-dev --lock` afterward.
- Kept Composer JSON/lock hashes unchanged. Repeating the command preserved all
  four Composer/PHPM file hashes and reported that no changes were needed.
- The host PATH PHP remained 7.4.33; global Composer was unchanged.

New unit/native fixtures cover constraint filtering before version-directory
requests, Composer caret/OR/tilde/exclusion intersections, bundled-version
rejection, provider/polyfill handling, Zend OPcache mapping, unsupported platform
requirements, stale/unverifiable locks, existing-version mismatches and CLI flag
validation. A real Redis DLL served through the in-memory HTTP transport verifies
that a second acquisition failure retains the entire old project environment,
cached artifacts can be reused, Composer edits after selection abort the commit,
and successful installation preserves PHP/INI pins without running project
scripts or autoload files. This Redis integration fixture explicitly skips when
Windows x64 or the opt-in Redis 6.1.0 DLL is unavailable.

The Go suite, native Windows integrations, Go vet and PHP adapter syntax checks
passed. Both Composer 2.7.1 and 2.10.3 were used in native runs. Updated Linux
amd64 and macOS amd64/arm64 binaries were cross-compiled; native runs on those
systems and the updated CI workflow were not performed locally.

## Linux execution attempt (2026-10-28)

Rebuilt the Linux amd64 CLI and cross-compiled its Go test executable on Windows.
These are compile checks, not Linux execution results.

The host has an existing Debian WSL2 registration, but starting it failed with
`Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_SERVICE_NOT_AVAILABLE`.
The installed Docker client also could not connect to a running engine
(`//./pipe/docker_engine` was unavailable). No Linux test process ran, and no
Windows features, WSL installation or system settings were changed.

Added `scripts/test-linux.sh` to run the existing unit/native suite and Go vet
on a usable Linux machine with Go and PHP already installed. Its shell syntax
passed Git Bash validation, and its non-Linux guard rejected this Windows host
with the expected exit status 2. Neither check is Linux execution evidence.
Linux runtime behavior still requires a usable WSL/VM/server. Composer/Redis
optional fixture skips must be reported rather than treated as verified coverage.

An external iStoreOS 24.10.8 x86_64 host was subsequently identified from user
output, with no PHP binary under `/usr/bin` and sufficient temporary storage.
Prepared `bin/phpm-linux-amd64-tests.tar.gz` containing static Go CLI/test binaries,
a POSIX runner and SHA-256 manifest. The runner supports explicit unit-only mode
without Go/PHP and a PHP-enabled native mode; it does not install host packages.
Local shell syntax, platform rejection and packaged file hashes passed checks.
Execution on that host is pending the user's upload/run and returned logs; no
Linux or iStoreOS pass result is claimed from preparing the bundle alone.

### Returned iStoreOS unit-test results

The user subsequently returned the full `--unit` log from iStoreOS 24.10.8,
Linux 6.6.144 x86_64. All four bundle file checksums passed. The Linux CLI ran
`version` and `help` successfully. The precompiled test binary reported 37
top-level passing tests, one top-level skip (`TestNativeProjectIsolation`, because
PHP was not enabled for this run), no failures, final `PASS`, and shell exit 0.

This is real Linux CLI/unit execution evidence from the user-operated host.
It covers parsing, paths, file replacement, project locks/transactions, artifact
integrity, resolver logic and mocked provider/Composer planning behavior. It does
not establish actual PHP execution, native SO loading, Composer integration or
upgrade activation/rollback on Linux. Those native fixtures remain pending PHP
setup and a returned `--native` run. Go vet was not part of the prebuilt run.

The first returned native attempt stopped after bundle checksums, with exit 127
and no PHP/test output. Inspection found that an unresolved explicitly configured
PHP path could abort the shell assignment under `set -e`, before the intended
missing-PHP message. The runner now tolerates lookup failure only to issue an
explicit missing/non-executable PHP diagnostic and exit 2; it still refuses to run
native tests without PHP. The bundle was refreshed. A simulated platform/lookup
check in Git Bash verified this guard and message; it is not Linux PHP execution
evidence. The actual device PHP installation/status still needs confirmation.

### Linux automatic runtime bootstrap (2026-10-28)

Added a Linux x86_64 NTS runtime provider using the third-party StaticPHP common
preset. It selects only archives with published SHA-256 files, validates the
download and static ELF architecture, then probes PHP before registering it. It
does not invoke the system package manager or a remote installer. Range transfers
resume interrupted reads and commit the cache only after verifying the complete
published digest. Unit fixtures cover malformed catalogs/checksums, origin/path
restrictions, transfer interruption/protocol errors, corrupt cache rejection,
unsafe tar entries and invalid/dynamically linked ELF files.

The Windows native suite and Go vet passed with PHP 8.4.27, Composer 2.10.3 and
the existing Redis fixtures. An actual StaticPHP 8.4.27 archive passed its published
digest, tar extraction and static ELF checks:
`c4f5986f0839d850f60a678805023793ef4f15a9ad493d324381fe8081640175`.
The opt-in Go live-provider test also passed on Windows in 277.16 seconds after
resuming interrupted reads and receiving all 13,721,980 bytes. These archive
checks do not execute Linux PHP. Updated Linux amd64 CLI/test and macOS
amd64/arm64 binaries cross-compiled successfully.

The refreshed prebuilt bundle includes a pinned Composer 2.10.3 fixture and a new
`sh run-tests.sh --bootstrap` mode. In disposable directories it downloads PHP
8.4/8.5, checks Composer requirements, prepares and activates the upgrade, rolls
back, verifies exact project-file restoration, then runs the native/unit suite.
Its shell syntax and all extracted bundle hashes passed local checks. The bundle
SHA-256 is `102e6a573944db2aff0b627c3fd3bd72d7b35d961647b4292a658260bf757123`.

The new bootstrap/native path has not yet run on iStoreOS; its real Linux PHP,
Composer and upgrade/rollback results depend on the next user-operated run.
The added Linux bootstrap CI job has not been executed. Prior iStoreOS unit
results above apply to the earlier bundle, not this new provider's native path.

### Debian official-source build preparation (2026-10-28)

Implemented the stage-A Debian 13 x86_64 source-build recipe under `build/linux`
and the manual `Debian PHP source build (stage A)` Actions workflow. The user
selected preparing Actions for execution after uploading the project. No GitHub
repository or Release was created and no workflow was dispatched from this host.
The client provider selection and existing binaries are unchanged by this work.

Actual local checks completed:

- Downloaded PHP 8.4.27 source from php.net; its SHA-256 matched the official
  `dcf93923b0e38e0d5252b25b796520b5a47761a08b3488b6d0b5d1115764d3f0`.
- Verified its real detached signature using the vendored public key whose full
  fingerprint matches the PHP download page:
  `9D7F99A0CB8F05C8A6958D6256A97AF7600A39A6`.
- Downloaded Redis 6.3.0 from the official PECL HTTPS origin and fixed its observed
  SHA-256 (`0d5141f634bd1db6c1ddcda053d25ecf2c4fc1c395430d534fd3f8d51dd7f0b5`).
  This is a reviewed input pin, not a claim of an independent Redis signature.
- Verified the pinned Composer fixture; inspected both real source archives for
  non-regular entries before adopting the strict extraction policy.
- Ten build-helper tests passed, including archive traversal/link/size rejection,
  timestamp preservation, deterministic archive metadata, actual shell prefix
  relocation, real source verification and rejection of a forged signature. No
  optional test was skipped in this local run.
- All six Python files parsed successfully. The workflow YAML, manual-only
  trigger, read-only repository permissions and full-commit Action pins passed a
  local validation using the existing Go YAML dependency.
- `go test ./...` and `go vet ./...` passed. Native PHP fixtures were not enabled
  for this Go run; these results do not execute the new Linux packages.

WSL still cannot start (`HCS_E_SERVICE_NOT_AVAILABLE`) and the Docker daemon is
unavailable. A local build attempt stopped with the expected Linux-engine error
before creating output packages. The pinned image/snapshot have not been pulled
or used for a real build here. PHP compilation, upstream test subset, relocated
SDK Redis rebuild, fresh-container PHP/extensions/Redis/TLS/Composer/FPM checks and
payload activation/rollback are implemented gates, not recorded pass results.

The next evidence must be an actual Actions build and its reports. Even a green
run will leave independent-VM validation, real MySQL connectivity, cross-version
PHP upgrades, signed distribution and production service switching outstanding.
The lifecycle fixture deliberately uses two payload revisions of the same PHP
version. Stage-A artifacts are unpublished and unsigned.
