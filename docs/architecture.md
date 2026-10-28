# Initial architecture and next milestones

Scope: the 0.1.0-dev CLI implementation, not a promise of complete platform support.

## Implementation boundaries

- `config.go`: strict project schema, project discovery, canonical hashing, file writes.
- `store.go`: PHP build identity, package copying/hashing, runtime selection and probes.
- `extension.go`: local module artifacts, loader kind, compatibility/version validation.
- `extension_windows_download.go`: Windows official extension catalog matching,
  archive downloads/receipts and bundled module selection.
- `extension_install.go`: project extension installation with runtime/extension pins.
- `project_transaction.go`: recoverable two-file configuration/lock commits.
- `upgrade.go` / `upgrade_cli.go`: candidate preparation, artifact rebinding,
  snapshot guards, explicit project activation and retained rollback checkpoints.
- `environment.go`: platform locks, selection, staged INI environments, actual-load checks.
- `download.go`: official Windows release adapter, checksummed cache, safe ZIP extraction.
- `runtime_download.go` / `linux_download.go`: OS dispatch and the StaticPHP Linux x64 NTS common provider; published-checksum catalog, restricted redirects and static ELF validation.
- `archive_tar.go`: bounded gzip/TAR extraction with trailer verification and no links/special files or overwrites.
- `linux_transfer.go`: bounded HTTP range chunks, interrupted-transfer continuation, final publisher SHA-256 and verified cache commit.
- `catalog_source.go` / `internal/catalog`: explicitly pinned Ed25519 sources,
  catalog expiry/sequence checks, Debian host gating, signed runtime/extension
  acquisition and exact-lock hydration. No automatic trust-root bootstrapping.
- `cli.go`: CLI dispatch and user-visible scope.
- `platform_*.go`: file replacement and process execution behavior.

Artifact identity conservatively binds extensions to one complete runtime build.
An extension version alone is not sufficient to reuse a DLL/SO. A future backend
may broaden reuse only with evidence for ABI and external-library compatibility.
Runtime payload identity includes filenames, executable bits and content; manifests
record PHP API, Zend build, ZTS/debug state, compiler/configure data when available.
Static extension lists are captured by running PHP without configuration.

Environment IDs derive from platform, runtime identity/digest, extension identities,
loader modes, effective INI and the project configuration hash. Machine-local INI
paths are generated, not recorded as portable lock requirements. The `targets` map
allows multiple platform entries in one JSON lock; a configuration change makes
old target entries stale until resolved on those platforms. Binary metadata, not
the coarse platform map key alone, determines whether artifacts match.

The store is per user by default. It does not assume administrator/root privileges,
change an existing PHP installation or manage operating-system packages. Imported
packages cannot contain symlinks. External dynamic libraries remain host concerns.

## Installation transaction

1. Acquire an exclusive project lock.
2. Read and validate the manifest and existing lock.
3. Resolve installed artifacts or retain a matching platform lock.
4. Check manifest identities and hash all runtime/extension payloads.
5. Stage generated configuration under PHPM_HOME, with an explicit empty scan dir.
6. Start PHP and verify the loaded configuration and exact extension map.
7. Commit the environment directory without overwriting an existing environment.
8. Recheck the project configuration and replace its lock file using a same-directory
   temporary file; failed preparation leaves the previous lock intact.

Project locks prevent PHPM writers using the same home from racing. They do not
coordinate arbitrary editors or a second independent PHPM_HOME. The configuration
is reread before commit, but external edits cannot be made transactional with the
CLI. Crashes can leave unreferenced artifacts or exclusive lock files. No operation
automatically reclaims a stale lock or removes active environments.

Before `exec`, PHPM repeats integrity and actual-load verification. It supplies an
absolute PHP binary and project INI, plus child-only PATH/PHPRC/PHP_INI_SCAN_DIR.
This is configuration isolation, not an OS security boundary. User PHP options,
application code or an absolute alternative executable can override it.

`ext install` first chooses the existing locked PHP when it still satisfies the
manifest. It acquires a built-in/bundled module or a matching official Windows
archive, preserves other matching extension pins, and probes the entire proposed
environment. YAML comments are retained and the serialized manifest is decoded
again to catch alias-driven changes to unrelated settings. Only then are the YAML
and lock replaced, under a project writer lock and a two-file recovery journal.
Failed writes restore both original files. Interrupted transactions block execution
until an install command restores the old pair; edits made after the interruption
are never automatically overwritten. Stale writer locks still require manual checks.

The Windows provider uses numeric version directories and exact archive filenames
that include PHP minor, TS/NTS, compiler and architecture. It does not guess ABI
from extension versions or execute remote install scripts. Initial extension archive
trust is official HTTPS plus an optional user checksum pin; observed hashes are
recorded as provenance/cache integrity data, not described as publisher signatures.

## Composer integration

The Composer launcher resolves a trusted PHP payload from PHPM_COMPOSER or an
absolute PATH directory and executes it directly with the verified project PHP,
INI and child environment. Windows batch wrappers cannot select the host PHP.
Composer commands otherwise retain their own argument, stdio and exit semantics.

The separate read-only extension checker embeds a small PHP adapter and loads
only the trusted Composer PHAR's classes. It does not construct Composer Factory,
load project autoload files/plugins/scripts, contact package repositories or write
Composer configuration. Snapshot JSON is sent over stdin to the selected PHP;
inputs are checked again for concurrent changes before reporting. Composer's
own PHAR deprecations are recorded by a narrowly scoped PHP error handler and
returned as structured diagnostics plus a warning; other PHP diagnostics retain
normal error handling and fail the inspection. Project error settings are unchanged.
Composer's ArrayLoader, platform repository and constraint matcher inspect root and locked
package requirements, including providers/replacers and explicit lock aliases.
No platform overrides are passed. Missing locks limit scope to root requirements;
stale locks are reported as failure. This is not full dependency resolution.

`ext install --missing` plans only missing `ext-*` requirements in a verified
current environment. Existing-version mismatches and non-extension failures are
refused before acquisition; stale/unverifiable Composer locks are also refused.
The adapter can filter candidate versions using every collected Composer
constraint. The existing provider applies that filter before querying release
directories, and to bundled versions before import. PHP/build pins remain intact.
Single and batch extension installs share one project transaction; final loading
and Composer checks complete before committing. Composer input snapshots must
match the initial inspection. Failed batches can leave immutable cached artifacts
but do not partially update project YAML or locks. This path never runs Composer
Factory, project scripts/plugins/autoload files or a dependency solver.

## Next milestones

Project upgrade preparation/activation/rollback are implemented. Preparation only
writes immutable artifacts and a home-local candidate, preserving project YAML/lock.
The reviewed candidate ID includes the base snapshots and complete target lock;
activation revalidates both environments and commits the two project files using
the existing journal. A restore point is saved before switching, covering process
interruption after the project-file commit. Rollback requires the current files to
match that checkpoint's target and verifies the retained old environment first.
Source preparation time is not treated as proof of application deployment or health.

1. Add a PIE backend with a separately identified control PHP. Validate artifact
   collection and build-only operations before permitting any shared-runtime writes.
2. Design extension dependencies, loading order, build options and explicit selection
   of multiple builds of the same extension version.
3. Add Linux source builds against declared distro/libc baselines with matching
   phpize/php-config/headers, then validate on Linux hosts.
4. Validate native macOS ARM64/Intel packages and external library behavior.
5. Add trusted artifact distribution so locked environments can be hydrated on
   another machine; distinguish exact artifact restoration from source rebuilds.
6. Add FPM as separate masters per extension environment, with independent sockets,
   PID files and configuration. Pools alone are not extension-version isolation.
7. Add service-manager integration, environment pinning, retained releases and
   application health verification before offering production activation/rollback.

Do not add service restarts, garbage collection or automatic privilege elevation
as a side effect of the existing install/verify commands.

### Debian source-build experiment

`build/linux` now contains a separate stage-A source-build/verification pipeline,
invoked manually by `debian-source-build.yml`. It pins official PHP/Redis sources,
the PHP release key, Debian amd64 image and signed archive snapshot. PHP is staged
under a non-existing build prefix; runtime/SDK/extension packages contain regular
files only. SDK prefix scripts are relocated and used to rebuild Redis. Compilation
has no network after source verification. A separate Debian stage with runtime
libraries but no PHP/compiler checks unprivileged CLI, project isolation, Composer,
Redis/TLS and a real FPM request before packages can be exported.

This pipeline is not a published runtime backend. The client still uses its
existing providers and local imports. Exported stage-A archives are unsigned;
the first hosted build passed SDK rebuilding and fresh-container runtime/FPM
checks (see `validation.md`). Independent-VM validation, cross-version upgrades,
trusted distribution and production service management remain separate work.

### Opt-in signed distribution

Stage B is a separate client/provider path, selected by a registered source name.
Registration pins a local public key and local catalog path or HTTPS URL. Catalogs
bind target, PHP API, build revision, extension loader, package sizes and hashes;
signatures cover their exact JSON bytes. Accepted sequence/hash state is persisted
under a source lock. Expired catalogs, rollbacks, changed same-sequence bytes and
invalid signatures fail without switching to another provider. This is a single
key/expiry/sequence protocol, not full TUF or an automatic key-rotation system.

Only Debian 13 amd64 with glibc 2.41 is accepted before downloaded PHP executes.
Package SHA-256 and size checks precede bounded extraction; binary hash, PHP ABI
and real relocated execution precede store import. A signed-catalog install may
update the provenance of an already identical local runtime; payload identity,
existing environment identity and project files do not change. Upgrade preparation
inherits the signed source; external modules bind to the exact runtime build.

`install --frozen --source NAME` looks up the locked build and hashes, restores its
runtime and bundled/catalog modules and compares their final IDs/digests with the
lock. It never chooses a newer build, rebuilds source or rewrites the project pair.
Normal frozen installation revalidates after hydration. Failed restoration may
leave verified cache/store entries. Existing manual-only local artifacts still
need explicit imports. Missing dependencies remain documented host requirements.

`cmd/phpm-release` signs offline only after checking native gate reports and package
hashes. The stage-B Actions verifier reuses a successful stage-A run and generates
an ephemeral test key inside the test process; its private key is never exported.
An optional separate write-permission job creates only a draft prerelease ZIP.
Production signing keys, independent-VM acceptance and production service control
remain outside this workflow. See `distribution.zh-CN.md` for exact commands.

## Sources used for the initial adapters

- [PHP command-line options](https://www.php.net/manual/en/features.commandline.options.php)
- [PHP INI scanning](https://www.php.net/manual/en/configuration.file.php)
- [Windows extension compatibility](https://www.php.net/install.pecl.windows.php)
- [Official Windows catalog](https://downloads.php.net/~windows/releases/releases.json)
- [PIE documentation](https://php.github.io/pie/)
- [phpize](https://www.php.net/manual/en/install.pecl.phpize.php)
- [FPM startup implementation](https://github.com/php/php-src/blob/PHP-8.4/sapi/fpm/fpm/fpm_main.c)
- [Composer platform requirements](https://getcomposer.org/doc/03-cli.md#check-platform-reqs)
- [Composer platform dependencies](https://getcomposer.org/doc/articles/composer-platform-dependencies.md)
