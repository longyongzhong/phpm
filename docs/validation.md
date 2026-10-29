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

### First private GitHub executions (2026-10-28)

The user created `longyongzhong/phpm` as a private repository and completed the
initial push. Subsequent API reads confirmed private visibility. Source changes
were committed and pushed without including `.cache`, local runtimes or binaries.
No public Release or visibility change was made.

Real hosted runs uncovered portability problems absent from the local Windows
fixture: PHP canonicalizes Windows short paths and macOS directory aliases;
comparing their textual spelling rejected the correct INI file. Verification now
requires both absolute paths to identify the same existing regular file using
`os.SameFile`. Different files with identical contents, missing files and directory
paths are rejected. Native PHP executable assertions use the same identity check.
The StaticPHP fixture also now expects modules already compiled into its runtime,
while continuing to check independently imported extension versions when supplied.

Composer checks canonicalize the selected PHAR path before forming its archive URL
and deprecation boundary. Windows archive spelling uses the filesystem's usual
case-insensitive comparison. A native fixture proves that a deprecation emitted
outside the selected PHAR is still a checker failure. Hosted CI supplies a pinned
Composer PHAR explicitly, since the macOS setup can provide an unpacked launcher.

The [CI run on a581ad4](https://github.com/longyongzhong/phpm/actions/runs/40664528880)
passed Windows, macOS and Ubuntu native fixtures plus Go vet. Its Linux bootstrap
also passed an actual StaticPHP 8.4.27 -> 8.5.12 -> 8.4.27 lifecycle. This is hosted
Linux evidence, not an iStoreOS execution result. The subsequent code commit
`3f75983` also passed [all four CI jobs](https://github.com/longyongzhong/phpm/actions/runs/40666364363).

The first Debian source run stopped because Debian's minimal dependency closure
did not include `gpgv`; it is now an explicit build package. The next source run
verified official inputs, compiled PHP and Redis, and passed relocated SDK Redis
rebuilding/loading. Its selected upstream PHP test subset reported 289 passes,
98 skips, one upstream expected failure, zero warnings and zero unexpected
failures (388 cases total). It then correctly failed the fresh-container HTTPS
gate: the Python fixture omitted a response length and OpenSSL rejected socket EOF.
The fixture now sends HTTP/1.1 with Content-Length. A regression test reads two
complete responses on one connection, and local real PHP cURL/OpenSSL HTTPS
requests passed with the explicit CA and normal certificate verification.

### Successful Debian source build (2026-10-28)

[Run 40666487813](https://github.com/longyongzhong/phpm/actions/runs/40666487813)
passed on source commit `3f75983cc1059d80559a55ace06ec5beb9ce8a59`.
The build/SDK/fresh-container step took 9 minutes 57 seconds. It verified the pinned
PHP source hash and release signature and produced PHP 8.4.27 NTS CLI/FPM, matching
SDK and Redis 6.3.0 packages for Debian 13 x86_64 (glibc 2.41). The upstream subset
again reported 289 passes, 98 skips, one expected failure and no unexpected
failures or warnings. Build-helper tests reported 10 passes and one optional
real-source fixture skip; source signature verification ran in the actual build.

The SDK report records all six checks passing, including rebuilding and loading
Redis after relocation. The fresh-container report records all 17 checks passing:
archive and ELF dependencies, non-root relocated PHP, PHPM imports, bundled and
separate extensions, frozen install, SQLite, DNS, two-project INI/extension
isolation, real Redis read/write, CA-verified cURL/OpenSSL HTTPS, Composer platform
checks, a non-root FastCGI/FPM request, and payload activation/rollback. The last
fixture uses two payload revisions of the same PHP version; it is not evidence
for a Debian cross-version upgrade.

Build ID: `php-8.4.27-debian13-x86_64-nts-b1-34e4dcbaefca`.
Artifact `12806239145` was downloaded locally and checked against GitHub's ZIP
SHA-256 `e89d72cf95b8fddb37f07bc42cac728b69970057506ef64b89611b1d7bf46cde`.
All three enclosed package sizes and SHA-256 values matched `build-result.json`:

| Package | Bytes | SHA-256 |
|---|---:|---|
| runtime | 10018484 | `e8ca695386afe0cf038e2469234624be5b7381f6981abe64996a934d2d4b4bca` |
| SDK | 5100340 | `c23d27c1a64342ee0047a542e3f0a901fa6f3d542b57e5d910b93254ae8f5279` |
| Redis | 252028 | `3f34dbf0e7cbb4886d069607504db362f4b77ea082b710e18a413158d097334a` |

The private Actions artifact expires on 2026-11-11 and is not a public Release.
The downloaded copy is retained in ignored `bin/`; reports are retained in ignored
`.cache/`. These remain unsigned stage-A packages. Independent-VM testing, real
MySQL connectivity, Debian cross-version upgrades, signed distribution, a client
download backend and production service switching are outstanding. No iStoreOS
compatibility or production deployment result is implied by this Debian run.

### Signed distribution and exact-lock hydration (2026-10-28)

Source commit `6197e0f2e7840ebe852f23b9f244327df8689c8d` passed
[CI run 40681126574](https://github.com/longyongzhong/phpm/actions/runs/40681126574):
Windows/macOS/Ubuntu native PHP integration and vet, plus Linux bootstrap. Local
Windows native PHP 8.4.27, Composer 2.10.3 and Redis 6.1.0/6.2.0 fixtures also passed
`go test -count=1 ./...` (21.141 seconds for the PHPM package), followed by vet.
New tests check signatures, wrong keys, duplicate/unknown JSON fields, expiry,
sequence replay, changed same-sequence bytes, package hash/size and cached-file
corruption. The HTTPS unit fixture needs loopback networking; the initial sandbox
run was denied by Windows socket permissions, then passed with that access.

[Stage-B run 40681164443](https://github.com/longyongzhong/phpm/actions/runs/40681164443)
reused the successful stage-A run `40666487813`, checked its workflow/status and
downloaded its actual accepted PHP 8.4.27/SDK/Redis 6.3.0 packages. The independent
Debian 13 container had runtime libraries but no host PHP/compiler and ran without
root or external networking. Its signed client lifecycle passed (4.24 seconds;
the complete container/build step took 57 seconds). It exercised a real loopback
HTTPS server and explicit public-key pin, signed PHP download/install, bundled
curl and signed Redis via Composer `--missing`, actual extension loading and
frozen install. It then restored the exact lock into an empty PHPM_HOME without
rewriting project files, prepared a source-inherited candidate, rebound its signed
extension and activated/rolled back the exact original project pair.

Negative native checks rejected altered signed metadata, a corrupt cache file,
an older correctly signed catalog and an expired catalog, while preserving active
project files. The alternate payload is a same-version test marker fixture; its
catalog is created directly by test code, without fabricating native build gate
reports. That fixture package is not exported as a release artifact.

The workflow generated an ephemeral Ed25519 signing key in test memory and exported
only the public key, signed catalog, unmodified original packages and verification
report. Its test catalog has a one-hour lifetime. Production signing keys are not
created or configured by this run. A separate least-permission job created draft
prerelease `stage-b-test-40681164443` (release ID `426591726`); API inspection confirmed
`draft=true` and `prerelease=true`. The repository remained private and no Release
was published. Its ZIP asset is 15194184 bytes with GitHub-reported SHA-256
`398db664d70faacd7b8a3c4998978f55e4351191c7a0a63ed3fedbb4606ebdd9`.

Local maintainer-tool smoke checks also signed the accepted stage-A packages with
a separate local test key, registered that source in an isolated inspection home
and listed its catalog. The Windows client correctly refused to install Debian
packages. Updated Windows/Linux CLI and offline signing-tool binaries were built
under ignored `bin/`; no business project or system PHP was modified.

These results do not establish a production trust root, private-GitHub asset API
authentication in the client, independent-VM compatibility, real cross-version
PHP upgrades or service deployment/health rollback. The single-key protocol does
not implement TUF, automatic key rotation or revocation. See
[the distribution guide](distribution.zh-CN.md) for the explicit trust decision and
current installation/restoration commands.

### Final transfer and artifact verification (2026-10-28)

Large-file downloads on this workstation repeatedly timed out, while bounded
range requests progressed. Signed remote packages now reuse the strict range
reader already used by StaticPHP. A TLS regression deliberately truncates the
first 1 MiB response after 4096 bytes, verifies the next request starts at byte
4096 and checks the complete final hash before cache commit. Existing range
protocol checks, the full Go suite and vet passed after this change.

Code commit `e48bd97522817455bfc1591492fcd4ff43815f64` passed all four jobs in
[CI run 40684786008](https://github.com/longyongzhong/phpm/actions/runs/40684786008)
and repeated the complete fresh-Debian signed distribution lifecycle in
[run 40684837869](https://github.com/longyongzhong/phpm/actions/runs/40684837869).
The latter left draft creation disabled, avoiding a duplicate draft. Windows/Linux
client and maintainer binaries under `bin/` were rebuilt from this clean code commit.

The first draft's complete ZIP was downloaded using bounded requests and matched
its GitHub asset SHA-256 recorded above. Each of its three package sizes/hashes
matched the signed catalog. The 16-check distribution report identifies the test
public-key SHA-256 as
`7900325fd8cc543d039391505f0ef75e7df790cee37270f3b420d1a5a81a4a4b`.
The rebuilt client then cryptographically verified the downloaded catalog and
registered/listed it in an isolated local inspection home using that test key.
The local ZIP is `bin/phpm-debian13-signed-stage-b-684795542.zip` (15194184 bytes).
Its test catalog expires at `2026-10-28T12:36:59Z`; source-registration/acquisition
after that date must use a newly signed catalog. The local maintainer smoke-test
seed was deleted after use; its public key and already signed bundle were retained.
Final API inspection still confirmed private repository visibility.

### Real PHP 8.5 source build and VM acceptance (2026-10-28)

The user selected built-in acceptance only; their business project is deferred.
The pinned second source recipe uses official PHP 8.5.12 tar.xz SHA-256
`08bc2eba2a588654ade1357174aa646b761556c862dda0678345ee29ed481209` and the
release-manager public key documented in `build/linux/keys/README.md`.
PHP 8.5's built-in OPcache is checked explicitly rather than accepting a missing
shared module as a successful build.

[Source run 40691767507](https://github.com/longyongzhong/phpm/actions/runs/40691767507)
passed on `c5830e75830cff9e640e39202e02288b1453d0b3`. The upstream test subset
reported 348 passes, 101 skips, one expected failure, zero unexpected failures and
zero warnings (450 cases). The matching relocated SDK rebuilt/loaded Redis, and
all fresh-container runtime/Composer/FPM gates passed. Build ID:
`php-8.5.12-debian13-x86_64-nts-b1-76c8d1160db5`.

The two-version source changes and the VM controller commit `40896f6` passed their
respective cross-platform CI runs `40691701891` and `40695238545`. Local Python
helper tests reported 15 passes and one optional source-input skip, including fixed
VM/MySQL inputs, cloud-init setup and bounded HTTP range/path validation.
Workflow YAML, action pins, read-only permissions and extracted shell steps passed
local syntax/scope checks. These checks alone do not establish VM acceptance.

The first VM run `40695290229` booted the fixed Debian image with its own kernel,
set up the owned MySQL instance, installed PHP 8.4 and prepared a real PHP 8.5
candidate. Both CLI database/Redis probes and the old FPM probe reached success.
It then failed the negative old-Redis-SO import assertion: the import was refused,
but PHP's startup warning reached stdout and was replaced by a JSON parse error,
losing the expected ABI detail. The assertion was retained. Extension-only `-n`
probes now explicitly route startup diagnostics to stderr without changing the
project's INI or application execution flags. Native missing-module checks cover
both ordinary/Zend loaders, with opposite project `display_errors` settings.
The Windows native PHP/Composer/two-Redis regression and vet passed after this fix
(25.245 seconds for the PHPM tests). A new VM run is required for final acceptance.

[VM run 40697707730](https://github.com/longyongzhong/phpm/actions/runs/40697707730)
passed on `2b01d471953db8f3b0d4e364e0989134d6adaace`, as did
[all cross-platform CI jobs](https://github.com/longyongzhong/phpm/actions/runs/40697638030).
The downloaded report artifact `12820682063` matched its GitHub SHA-256
`f23ed2c6ed5bdca309f805b02c5d11a3be763cf1659be9cbcabac7a99f443f52`.
It contained only the two JSON reports and three diagnostic logs, with no private
keys. The report records 21 completed checks and these observed results:

| Evidence | Observed value |
|---|---|
| Host kernel | `7.0.0-1017-azure` |
| Independent guest kernel | `6.12.113+deb13-cloud-amd64` |
| Guest virtualization / libc | QEMU / glibc 2.41 |
| Actual version lifecycle | `8.4.27 -> 8.5.12 -> 8.4.27` |
| PHP extension API | `20240924 -> 20250925` |
| Actual database server | MySQL `8.4.13` |
| Redis extension | `6.3.0`, independently acquired for both PHP builds |
| Bounded active-version soak | 41 CLI/FPM rounds in 122.36 seconds (120 requested) |

Both real versions executed prepared MySQL queries, utf8mb4 data and transaction
rollback checks, Redis read/write and FPM requests with the project INI. The old SO
was rejected with its actual API diagnostic, while the valid candidate remained
verifiable. OPcache moved from a shared module to the 8.5 built-in. Activation left
the old CLI/FPM processes on 8.4; rollback restored the exact original environment
ID and YAML/lock bytes while retaining MySQL/Redis data. The guest independently
checked its boot identity differed from the host, had no system PHP/compiler,
ran non-root and had no default internet route during acceptance. Cleanup completed
successfully as part of the green job.

Reports are retained locally under `.cache/vm-report-12820682063/`. Windows and
Linux CLI binaries in `bin/` were rebuilt with the diagnostic fix. This closes
the built-in independent-VM/real-version/MySQL acceptance milestone, not business
application acceptance, sustained high-concurrency qualification, production
service/traffic switching or formal signing-key operations. Those remain deferred.
The workflow requires explicit successful base/target run IDs rather than keeping
a default reference to an artifact that will expire after its retention period.

## Offline-root authorization and client distribution (2026-10-28)

The operator chose to generate and back up both formal root and publishing private
keys offline. No formal key was generated, stored in GitHub Secrets, or published
by this work. CI creates disposable, short-lived test keys only. The repository
was confirmed private after the final draft upload.

`029773ce7d54c8d7ca04c0822a901886489812d3` added role-separated Ed25519 root
authorization, publishing-key rotation/revocation, persistent source rollback and
revocation state, explicit manual root replacement, signed client archives with
full dependency notices, and a Python/OpenSSL bootstrap verifier. Legacy direct-key
sources remain compatible and root-authorized sources cannot downgrade to them.

Local Windows native PHP/Composer/two-Redis regression passed (`internal/phpm`
24.899 seconds), along with vet and independent Go→Python/OpenSSL signature
verification for both archive formats. The 16 Linux helper tests completed with
15 passes and the optional real-upstream-signature-input test skipped; prior
stage-A upstream signature verification remains separately recorded above.
Bootstrap tests reject duplicate JSON, unsafe archive entries, links, duplicate
entries and missing notices. Final tests also cover bounded Windows rename retries,
existing destinations, private-only draft creation, existing releases and uploaded
asset digest mismatch. Unit fixture bytes are not native runtime evidence.

[Independent VM run 40716970516](https://github.com/longyongzhong/phpm/actions/runs/40716970516)
passed on `029773c` with 23 checks. The downloaded artifact `12828378248` matched
SHA-256 `8ea226b194c647cd50d66373dc1da486634322a91ba4c1910ddb6b35e22265ce`
and contained only the two JSON reports and three logs. Reports are retained at
`.cache/vm-report-12828378248/` locally. The non-root Debian guest installed the
signed Linux client over TLS through the Python/OpenSSL installer, using an
ephemeral root fingerprint delivered by the controlled SSH fixture, registered
the root-authorized PHP source and completed the full existing real-version
lifecycle. Observed versions were `8.4.27 → 8.5.12 → 8.4.27`; both CLI and FPM
exercised MySQL 8.4.13 and Redis, including 41 soak rounds in 122.87 seconds.
Original project bytes and database data survived rollback. This is built-in,
bounded acceptance, not business application or long-duration load qualification.

[All four CI jobs on 029773c](https://github.com/longyongzhong/phpm/actions/runs/40716881154)
passed. The first draft rehearsal uploaded 23 assets but failed its final check:
GitHub's release-by-tag endpoint does not return unpublished drafts. The publisher
now retains the creation response's numeric Release ID and checks that exact
draft, asset names, sizes and SHA-256 digests; it does not skip final verification.
[The corrected rehearsal](https://github.com/longyongzhong/phpm/actions/runs/40718128089)
and [its cross-platform CI](https://github.com/longyongzhong/phpm/actions/runs/40718120035)
passed on `3c8a87e`.

The real Windows client from the first draft was downloaded with GitHub asset
digest checks, independently verified by the installer and executed successfully
from `.cache/client-preview-windows-retry/`. The first final-directory rename
returned Windows error 5; a fresh attempt succeeded. The installer now retries
only Windows sharing/access conflicts at the final verified rename for at most
three seconds; validation failures and existing destinations are never retried
as successful installations. Archive and draft-boundary suites contain six and
four passing tests respectively.

[Final stage-D rehearsal 40718873415](https://github.com/longyongzhong/phpm/actions/runs/40718873415)
passed on `97600dda912087cc99abbbda4f0da509acddd0d9`, including the rename change
and publishing regression tests. [All four final CI jobs](https://github.com/longyongzhong/phpm/actions/runs/40718867675)
also passed on that commit (Windows, Linux, macOS and interpreter-free Linux
bootstrap). Subsequent edits only align CLI help text and record these results;
the source-trust checks and Windows CLI build passed after the help edit.
The rehearsal created private draft `426868100`, tag
`stage-d-test-40718873415`, with 23 individually uploaded, digest-verified assets:
[review the test draft](https://github.com/longyongzhong/phpm/releases/tag/untagged-6c3602f577e99817132d).
Its keys are disposable and metadata expires after 48 hours. These assets must
not be adopted as the formal production trust root. Earlier drafts are retained
as inspection evidence. No public release or production deployment occurred.

Formal operator commands and first-install trust limitations are documented in
`docs/offline-release.zh-CN.md`. Formal key generation/backup and offline signing
remain operator actions. Business-project acceptance, long-term qualification,
production FPM/traffic switching and additional OS/architecture targets remain
outside this milestone.

## Public repository and offline operator handoff (2026-10-29)

The user changed `longyongzhong/phpm` to public. The repository identity and
`private: false` were verified through the authenticated API before this work.
Draft upload now supports both visibilities, preserves the draft-only boundary,
rejects existing releases and checks the uploaded asset set, sizes and digests.
No repository visibility or existing release was changed by these scripts.

Commit `d7ec5504bfe39e1b261b8d12c5b5f0967a99156f` passed
[all cross-platform CI jobs](https://github.com/longyongzhong/phpm/actions/runs/40806254617).
Local native PHP/Composer/two-Redis regression and signature interoperability also
passed (`internal/phpm` 27.142 seconds), as did vet. The eight release-helper tests
passed locally with only the unavailable Windows symlink fixture skipped; Linux
CI ran that fixture. Release builds can embed the candidate version; ordinary
development builds continue to report `0.1.0-dev`.

[Offline materials run 40806351995](https://github.com/longyongzhong/phpm/actions/runs/40806351995)
successfully built candidate `0.1.0-rc.1` from the previously accepted stage-A
runs `40666487813` and `40691767507`. The artifact is
`phpm-offline-signing-materials-40806351995` (ID `12868855155`, 49,454,055 bytes),
GitHub archive SHA-256 `882aa7edf435a36b769a8c0437135ea60d74b54531ee7f6e25fc13d2f02fcf26`.
The **release-inputs.json** request SHA-256 used by the offline signer is
`6cb4fd070711fe58f6569ce57175678a2ac9fafa54940692626dff4945de3826`.
The archive hash and request hash name different objects and are not interchangeable.
The shipped offline script signed the real inputs using discarded test keys,
verified all 21 release assets, installed the real client and observed
`phpm version == 0.1.0-rc.1`. All materials were hashed and checked before upload;
no formal private key was used and no test private key was uploaded.

[Public-repository rehearsal 40806359085](https://github.com/longyongzhong/phpm/actions/runs/40806359085)
also passed, creating draft `427400608`, tag `stage-d-test-40806359085`, with 23
digest-verified assets. This is an unpublished test-key draft, not the operator's
formally signed release. Instructions for operator-held keys and one-command
offline signing are in `docs/release-quickstart.zh-CN.md`, also included in the
materials as `OFFLINE-GUIDE.md`. Formal signing remains pending operator action;
only the resulting public assets and independently checked public root/fingerprint
need to return to the connected publisher. Publishing the formal release remains
a separate decision after draft review.

## GitHub Environment publishing-key signing (2026-10-29)

The user changed the routine signing preference to GitHub Actions. The root
private key remains operator-held; the publishing seed is configured by the user
as Environment Secret `PHPM_RELEASE_SEED`. No formal seed was generated/read by
this work or sent through chat. Environment `phpm-release` was created and its
custom deployment branch policy was read back as exactly branch `main`. Existing
environment protection, if different, is preserved rather than silently weakened.
Secret metadata confirmed the formal publishing secret is not yet configured.

Commit `b10d13be211758f55885e6f37631549e60947e20` passed
[all cross-platform CI jobs](https://github.com/longyongzhong/phpm/actions/runs/40809113466).
The local native PHP/Composer/two-Redis regression passed (`internal/phpm` 22.107
seconds), as did vet, two Go initialization-helper tests and twelve Python release
tests (one Windows symlink fixture skipped locally and exercised on Linux CI).
Tests cover incomplete-pair preservation and reuse of matching keypairs, refusing
existing authorization outputs, keeping private/public directories separate,
secret-file removal on failure, removing the secret from child environments,
missing/invalid secrets, wrong source commit/repository/version and refusing
publication before any GitHub mutation when signed identity differs.

[Materials/signing run 40809196722](https://github.com/longyongzhong/phpm/actions/runs/40809196722)
passed both existing offline and new GitHub environment-variable signing paths.
The latter supplied only a disposable publishing seed to the real signing script,
verified the root-authorized release, installed its real Linux client and observed
version `0.1.0-rc.1`. Both test keypairs and the test signed bundle were temporary;
no test private keys or formal private keys were uploaded. The exact build request
hash for this run is `5a3a72e7ec9445ff49505aec68ca3dd4e0477e84e857c7d2b7b91ff1c2c04e63`.

`GitHub signed release draft` composes the reusable checked-material workflow,
an Environment-scoped read-only signing job, and a separate write-enabled draft
upload job without the signing secret. It pins the requested version/source,
uses the Actions run ID as the catalog sequence, refuses existing releases and
does not publish automatically. Formal execution of this entire workflow remains
pending the operator's one-time `phpm-release setup-github`, committing its four
public authorization files, and entering the publishing Secret in GitHub. The
local binary `bin/phpm-release.exe` was rebuilt with that initializer;
the initializer was not run against the operator's actual key directory.

## First operator-key signed release draft (2026-10-29)

The operator completed initialization and configured `PHPM_RELEASE_SEED`.
Only the four public authorization files were inspected and committed in
`ff6fbd0c3ed5506d268ea6ebfdf964e47817b5f7`. The root signature/fingerprint and
agreement with local public key files were verified without reading seed files.
Environment metadata confirmed the secret exists and only `main` is allowed.

[Release run 40814792546](https://github.com/longyongzhong/phpm/actions/runs/40814792546)
passed configuration, reusable material preparation, publishing-key signing and
draft upload. Its source commit also passed
[all CI jobs](https://github.com/longyongzhong/phpm/actions/runs/40811330061).
The version is `0.1.0-rc.1`; root fingerprint:
`b9aaa6269e68f95ea18147d09c043ca9c8ae332903e0bc04b23c34ad98e35d32`.
Authorization version is 1 and catalog sequence `40814792546` covers PHP
`8.4.27` and `8.5.12`.

Draft `427450758`, tag `v0.1.0-rc.1`, contains 23 assets, all checked for their
exact remote names, sizes and SHA-256 after upload.
[Review the draft](https://github.com/longyongzhong/phpm/releases/tag/untagged-a1dcc77c4d5ef263ba26).
It remains `draft: true`, `prerelease: true`; public issuance is a separate decision.

Eight selected assets were independently downloaded with GitHub digest checks to
`.cache/client-release-427450758/`. The catalog signature was verified against
the pinned operator root. The Python/OpenSSL installer verified the Windows
archive, installed it into `.cache/operator-client-0.1.0-rc.1/` and the executable
reported `0.1.0-rc.1`. Windows archive SHA-256:
`357ff41dded4598b685e5d4b4b5d5c130cd744a068c286d3ac03928b24ba6823`.
This did not change PATH, global PHPM or a business project. PHP payload hashes
were checked by the pipeline; their signed catalog was additionally checked locally.

Root authorization expires `2027-10-29T03:11:37Z`, client metadata expires
`2026-11-28T03:55:50Z`. These operator-key signatures differ from earlier test
rehearsals. No publishing-secret plaintext or local seed file was retrieved by
the agent.
