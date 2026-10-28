<?php
// Read-only platform diagnostics using the user's trusted Composer 2 PHAR.
// No project autoloading, Factory, plugins, scripts, repositories or HTTP clients.
$composerDeprecations = [];
try {
    if (!extension_loaded('phar')) {
        throw new RuntimeException('The project PHP needs the phar extension to inspect Composer.');
    }
    $autoload = 'phar://' . $argv[1] . '/vendor/autoload.php';
    if (!is_file($autoload)) {
        throw new RuntimeException('Extension checks require a Composer 2 PHAR; set PHPM_COMPOSER to its path.');
    }
    // Older Composer releases can still inspect requirements on newer PHP.
    // Record their deprecations as diagnostics instead of corrupting JSON or
    // treating them as checker failure. Other PHP diagnostics keep normal handling.
    $composerPrefix = str_replace('\\', '/', 'phar://' . $argv[1] . '/');
    set_error_handler(static function (int $level, string $message, string $file, int $line) use (&$composerDeprecations, $composerPrefix): bool {
        if (($level === E_DEPRECATED || $level === E_USER_DEPRECATED) && strpos(str_replace('\\', '/', $file), $composerPrefix) === 0) {
            $composerDeprecations[] = ['level' => 'deprecated', 'message' => $message, 'file' => $file, 'line' => $line];
            return true;
        }
        return false;
    }, E_DEPRECATED | E_USER_DEPRECATED);
    require $autoload;
    if (!defined('Composer\Composer::VERSION') || strpos(Composer\Composer::VERSION, '2.') !== 0) {
        throw new RuntimeException('Extension checks require Composer 2.');
    }
    $input = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
    $rootData = $input['root'];
    $lock = $input['lock'];
    $withDev = !$input['no_dev'];
    $report = ['scope' => $lock === null ? 'root' : 'root+lock', 'warnings' => [], 'results' => []];
    $links = [];
    $loader = new Composer\Package\Loader\ArrayLoader();
    $repository = new Composer\Repository\InstalledRepository([]);
    $metadata = static function (array $data): array {
        $keys = ['name', 'version', 'version_normalized', 'require', 'require-dev', 'provide', 'replace', 'conflict', 'default-branch'];
        $filtered = array_intersect_key($data, array_flip($keys));
        foreach (['require', 'require-dev', 'provide', 'replace', 'conflict'] as $type) {
            if (isset($filtered[$type]) && !is_array($filtered[$type])) {
                throw new RuntimeException($type . ' must be an object of package names and constraints.');
            }
            foreach ($filtered[$type] ?? [] as $name => $constraint) {
                if (!is_string($name) || !is_string($constraint)) {
                    throw new RuntimeException('Package links require string names and string constraints.');
                }
            }
        }
        if (isset($data['extra']['branch-alias'])) {
            $filtered['extra'] = ['branch-alias' => $data['extra']['branch-alias']];
        }
        return $filtered;
    };
    $collect = static function (array $requires) use (&$links): void {
        foreach ($requires as $name => $link) {
            if (Composer\Repository\PlatformRepository::isPlatformPackage($name)) {
                $links[$name][] = $link;
            }
        }
    };
    if (!isset($rootData['name'])) {
        $rootData['name'] = 'phpm/root-project';
    }
    if (!isset($rootData['version'])) {
        $rootVersion = getenv('COMPOSER_ROOT_VERSION');
        if ($rootVersion === false || $rootVersion === '') {
            foreach (['require', 'require-dev', 'provide', 'replace', 'conflict'] as $type) {
                if (!is_array($rootData[$type] ?? [])) {
                    throw new RuntimeException($type . ' must be an object.');
                }
                foreach ($rootData[$type] ?? [] as $name => $constraint) {
                    if (is_string($name) && is_string($constraint) && Composer\Repository\PlatformRepository::isPlatformPackage($name) && strpos($constraint, 'self.version') !== false) {
                        throw new RuntimeException('A platform self.version link requires root version or COMPOSER_ROOT_VERSION; VCS guessing is not performed by this read-only check.');
                    }
                }
            }
            $rootVersion = '1.0.0';
        }
        $rootData['version'] = $rootVersion;
    }
    $root = $loader->load($metadata($rootData), 'Composer\Package\RootPackage');
    $repository->addRepository(new Composer\Repository\RootPackageRepository($root));
    $collect($root->getRequires());
    if ($withDev) {
        $collect($root->getDevRequires());
    }
    if ($lock === null) {
        $report['warnings'][] = 'No Composer lock: root requirements only; transitive dependency requirements are not known.';
    } else {
        if (!isset($lock['packages']) || !is_array($lock['packages'])) {
            throw new RuntimeException('Composer lock has no valid packages list.');
        }
        $packages = $lock['packages'];
        if ($withDev) {
            if (!isset($lock['packages-dev']) || !is_array($lock['packages-dev'])) {
                throw new RuntimeException('Composer lock lacks development dependency metadata; use --no-dev or regenerate the lock with Composer.');
            }
            $packages = array_merge($packages, $lock['packages-dev']);
        }
        $locked = new Composer\Repository\LockArrayRepository();
        $byName = [];
        foreach ($packages as $data) {
            if (!is_array($data)) {
                throw new RuntimeException('Invalid locked package metadata.');
            }
            $package = $loader->load($metadata($data));
            $locked->addPackage($package);
            $byName[$package->getName()] = $package instanceof Composer\Package\AliasPackage ? $package->getAliasOf() : $package;
            $collect($package->getRequires());
        }
        foreach ($lock['aliases'] ?? [] as $alias) {
            if (!isset($alias['package'], $alias['alias_normalized'], $alias['alias'])) {
                throw new RuntimeException('Invalid Composer lock alias.');
            }
            if (isset($byName[$alias['package']])) {
                $locked->addPackage(new Composer\Package\CompleteAliasPackage($byName[$alias['package']], $alias['alias_normalized'], $alias['alias']));
            }
        }
        $repository->addRepository($locked);
        if (!empty($lock['content-hash'])) {
            $report['lock_fresh'] = hash_equals($lock['content-hash'], Composer\Package\Locker::getContentHash($input['root_json']));
        } elseif (!empty($lock['hash'])) {
            $report['lock_fresh'] = hash_equals($lock['hash'], md5($input['root_json']));
        } else {
            $report['warnings'][] = 'Composer lock freshness cannot be verified: no content hash is recorded.';
        }
        if (isset($report['lock_fresh']) && !$report['lock_fresh']) {
            $report['warnings'][] = 'Composer lock is stale; newly declared dependencies may have additional platform requirements.';
        }
    }
    // Intentionally do not apply config.platform or COMPOSER_IGNORE_PLATFORM_REQ*.
    $platform = new Composer\Repository\PlatformRepository([], []);
    $repository->addRepository($platform);
    ksort($links);
    foreach ($links as $name => $requirements) {
        if (isset($input['candidate_versions'][$name])) {
            $report['matching_versions'][$name] = [];
            $parser = new Composer\Package\Version\VersionParser();
            foreach ($input['candidate_versions'][$name] as $version) {
                $candidate = new Composer\Semver\Constraint\Constraint('==', $parser->normalize($version));
                foreach ($requirements as $requirement) {
                    if (!$requirement->getConstraint()->matches($candidate)) {
                        continue 2;
                    }
                }
                $report['matching_versions'][$name][] = $version;
            }
        }
        $native = $platform->findPackages($name);
        $row = ['name' => $name, 'version' => $native ? $native[0]->getPrettyVersion() : 'n/a', 'status' => 'missing', 'requirements' => []];
        foreach ($requirements as $requirement) {
            $row['requirements'][] = ['source' => $requirement->getSource(), 'constraint' => $requirement->getPrettyConstraint(), 'type' => $requirement->getDescription()];
        }
        $providers = $repository->findPackagesWithReplacersAndProviders($name);
        if ($providers) {
            $row['status'] = 'failed';
        }
        foreach ($providers as $provider) {
            $constraint = null;
            if ($provider->getName() === $name) {
                $constraint = new Composer\Semver\Constraint\Constraint('==', $provider->getVersion());
            } else {
                foreach (array_merge($provider->getProvides(), $provider->getReplaces()) as $link) {
                    if ($link->getTarget() === $name) {
                        $constraint = $link->getConstraint();
                        break;
                    }
                }
            }
            if ($constraint === null) {
                continue;
            }
            $satisfied = true;
            foreach ($requirements as $requirement) {
                if (!$requirement->getConstraint()->matches($constraint)) {
                    $satisfied = false;
                    break;
                }
            }
            if ($satisfied) {
                $row['status'] = 'success';
                if ($provider->getName() !== $name) {
                    $row['provider'] = $provider->getPrettyName();
                }
                break;
            }
        }
        $report['results'][] = $row;
    }
    if ($composerDeprecations) {
        $report['diagnostics'] = $composerDeprecations;
        $report['warnings'][] = 'Composer ' . Composer\Composer::VERSION . ' emitted ' . count($composerDeprecations) . ' deprecation notice(s) on PHP ' . PHP_VERSION . '; requirement checking completed. Update Composer or set PHPM_COMPOSER to a compatible PHAR. Details: --json.';
    }
    echo json_encode($report, JSON_THROW_ON_ERROR);
} catch (Throwable $error) {
    echo json_encode(['error' => 'Composer inspection failed: ' . $error->getMessage()]);
}
