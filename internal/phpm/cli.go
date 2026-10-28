package phpm

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const help = `PHPM — project PHP runtime and extension environments

Usage: phpm [--home DIRECTORY] COMMAND

  init --php 8.4                    Create phpm.yaml in the current directory
  php list                         List imported PHP builds
  php available                    Query the platform's checksummed PHP catalog
  php install 8.4 [--ts]            Download PHP (Windows x64 / Linux x64 static NTS)
  php import DIRECTORY [--binary bin/php]
                                   Copy a self-contained local PHP distribution
  php upgrade [VERSION] [--runtime ID]
                                   Prepare a verified candidate; keep current PHP
  ext list                         List imported extension builds
  ext check [--no-dev] [--candidate] [--json] [--lock]
                                   Inspect Composer PHP/extension requirements
  ext install NAME[@VERSION] [--sha256 HASH] [--zend]
                                   Download/enable extension and update the project
  ext install --missing [--no-dev] [--lock]
                                   Install missing Composer-required extensions
  ext import NAME@VERSION --file FILE [--runtime ID] [--package-dir DIRECTORY]
             [--sha256 HASH] [--zend]
                                   Import and probe a local DLL/SO
  install [--frozen | --update]      Verify artifacts and commit a locked environment
  info                             Print the verified current environment as JSON
  verify                           Verify locked files and actual PHP loading
  doctor                           Diagnose the current project (same checks as verify)
  info|verify --candidate           Inspect or verify the prepared upgrade
  diff                             Compare current and candidate environments
  exec [--candidate] -- php [ARGS...]
                                   Execute with current or candidate PHP
  composer [--candidate] [ARGS...]  Run trusted Composer with project PHP
  activate CANDIDATE_ID             Switch project files; retain a restore point
  history                          List retained upgrade restore points
  rollback [CHECKPOINT_ID]          Restore previous project files and environment
  version                          Print PHPM version

ext install updates phpm.yaml and phpm.lock after verification. ext import only
populates the store. Online downloads currently support official Windows x64 PECL
archives; runtime-bundled modules can also be enabled. No system PHP is changed.
Activation/rollback affect future launches only; service restarts stay manual.
Linux downloads use the community StaticPHP common preset with built-in modules.
Linux/macOS automatic compilation, PIE integration, FPM and deployment are not
implemented in this development release. See README.md for the support matrix.
`

type CLI struct {
	Store    *Store
	In       io.Reader
	Out, Err io.Writer
}

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, help)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, Version)
		return 0
	}
	home := ""
	if args[0] == "--home" {
		if len(args) < 3 {
			fmt.Fprintln(stderr, "phpm: --home requires a directory and command")
			return 2
		}
		home, args = args[1], args[2:]
	}
	store, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, "phpm:", err)
		return 1
	}
	cli := CLI{Store: store, In: stdin, Out: stdout, Err: stderr}
	code, err := cli.run(args)
	if err != nil {
		fmt.Fprintln(stderr, "phpm:", err)
		return 1
	}
	return code
}

// Accept flags on either side of positional arguments without invoking a shell.
func parseFlags(set *flag.FlagSet, args []string) ([]string, error) {
	set.SetOutput(io.Discard)
	var options, positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := set.Lookup(name)
		if option == nil {
			return nil, fmt.Errorf("unknown option %s", arg)
		}
		options = append(options, arg)
		boolOption, isBool := option.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && (!isBool || !boolOption.IsBoolFlag()) {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("option %s needs a value", arg)
			}
			options = append(options, args[i])
		}
	}
	return positionals, set.Parse(options)
}

func (c *CLI) run(args []string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("missing command")
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprint(c.Out, help)
		return 0, nil
	case "version", "--version":
		fmt.Fprintln(c.Out, Version)
		return 0, nil
	case "init":
		return 0, c.init(args[1:])
	case "php":
		return 0, c.php(args[1:])
	case "ext":
		if len(args) > 1 && args[1] == "check" {
			return c.checkComposerExtensions(args[2:])
		}
		return 0, c.ext(args[1:])
	case "composer":
		return c.composer(args[1:])
	case "install":
		set := flag.NewFlagSet("install", flag.ContinueOnError)
		frozen := set.Bool("frozen", false, "require an unchanged lock")
		update := set.Bool("update", false, "resolve installed builds again")
		pos, err := parseFlags(set, args[1:])
		if err != nil {
			return 0, err
		}
		if len(pos) != 0 {
			return 0, errors.New("install does not accept positional arguments")
		}
		root, err := findProject(".")
		if err != nil {
			return 0, err
		}
		env, err := c.Store.install(root, *frozen, *update)
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(c.Out, "Environment ready:", env.ID)
		return 0, nil
	case "info", "verify", "doctor":
		set := flag.NewFlagSet(args[0], flag.ContinueOnError)
		candidate := set.Bool("candidate", false, "use the prepared candidate")
		pos, err := parseFlags(set, args[1:])
		if err != nil {
			return 0, err
		}
		if len(pos) != 0 {
			return 0, errors.New("unexpected arguments")
		}
		root, err := findProject(".")
		if err != nil {
			return 0, err
		}
		var env LockedEnvironment
		var plan UpgradePlan
		if *candidate {
			plan, err = c.Store.candidate(root)
			env = plan.Environment
		} else {
			env, err = c.Store.current(root)
		}
		if err != nil {
			return 0, err
		}
		if args[0] == "info" {
			encoder := json.NewEncoder(c.Out)
			encoder.SetIndent("", "  ")
			if *candidate {
				return 0, encoder.Encode(map[string]any{"candidate": plan.ID, "created_at": plan.CreatedAt, "environment": env})
			}
			return 0, encoder.Encode(env)
		}
		if *candidate {
			fmt.Fprintln(c.Out, "Candidate:", plan.ID)
		}
		fmt.Fprintln(c.Out, "Verified:", env.ID)
		fmt.Fprintln(c.Out, "PHP:", c.Store.runtimeBinary(env.Runtime))
		fmt.Fprintln(c.Out, "INI:", filepath.Join(c.Store.environmentPath(env), "php.ini"))
		return 0, nil
	case "exec":
		return c.execute(args[1:])
	case "activate", "rollback", "history", "diff":
		return 0, c.upgradeCommand(args)
	default:
		return 0, fmt.Errorf("unknown command %q; use phpm help", args[0])
	}
}

func (c *CLI) init(args []string) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	version := set.String("php", "8.4", "PHP version")
	pos, err := parseFlags(set, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("init does not accept positional arguments")
	}
	cfg := Config{Schema: 1, PHP: *version}
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	file, err := os.OpenFile("phpm.yaml", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Created phpm.yaml; import/install PHP, declare extensions, then run phpm install.")
	return nil
}

func (c *CLI) php(args []string) error {
	if len(args) == 0 {
		return errors.New("expected php list, available, install, import or upgrade")
	}
	switch args[0] {
	case "upgrade":
		return c.preparePHPUpgrade(args[1:])
	case "list":
		if len(args) != 1 {
			return errors.New("php list does not accept arguments")
		}
		runtimes, err := c.Store.runtimes()
		if err != nil {
			return err
		}
		for _, rt := range runtimes {
			fmt.Fprintln(c.Out, formatRuntime(rt))
		}
		if len(runtimes) == 0 {
			fmt.Fprintln(c.Out, "No PHP builds installed.")
		}
		return nil
	case "available":
		if len(args) != 1 {
			return errors.New("php available does not accept arguments")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if runtime.GOOS == "linux" {
			if runtime.GOARCH != "amd64" {
				return errors.New("automatic Linux PHP downloads currently support x86_64")
			}
			releases, err := (linuxRuntimeProvider{Client: staticPHPClient()}).catalog(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(c.Out, "StaticPHP community common-preset Linux x64 NTS builds (published SHA-256 required):")
			for _, release := range releases {
				fmt.Fprintln(c.Out, release.Version)
			}
			return nil
		}
		catalog, err := fetchWindowsCatalog(ctx)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(catalog))
		for key := range catalog {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintln(c.Out, "Official Windows catalog (availability does not imply security support):")
		for _, key := range keys {
			fmt.Fprintln(c.Out, catalog[key].Version)
		}
		return nil
	case "install":
		set := flag.NewFlagSet("php install", flag.ContinueOnError)
		ts := set.Bool("ts", false, "thread-safe build")
		pos, err := parseFlags(set, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: phpm php install VERSION [--ts]")
		}
		if runtime.GOOS == "linux" {
			fmt.Fprintln(c.Out, "Fetching StaticPHP community static Linux x64 NTS build and published SHA-256; common extensions are built in...")
		} else {
			fmt.Fprintln(c.Out, "Fetching official catalog and checksum-verified PHP archive...")
		}
		rt, err := c.Store.installRuntime(pos[0], *ts, c.Out)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Installed:", formatRuntime(rt))
		return nil
	case "import":
		set := flag.NewFlagSet("php import", flag.ContinueOnError)
		binary := set.String("binary", "", "relative PHP executable")
		pos, err := parseFlags(set, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: phpm php import DIRECTORY [--binary RELATIVE_PATH]")
		}
		rt, err := c.Store.importRuntime(pos[0], *binary, Source{Kind: "local-import"})
		if err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Imported:", formatRuntime(rt))
		return nil
	default:
		return fmt.Errorf("unknown php command %q", args[0])
	}
}

func (c *CLI) ext(args []string) error {
	if len(args) == 0 {
		return errors.New("expected ext list, install, import or check")
	}
	if args[0] == "install" {
		set := flag.NewFlagSet("ext install", flag.ContinueOnError)
		checksum := set.String("sha256", "", "expected archive/module checksum")
		zend := set.Bool("zend", false, "load as Zend extension")
		missing := set.Bool("missing", false, "install missing Composer-required extensions as one batch")
		noDev := set.Bool("no-dev", false, "exclude Composer development requirements with --missing")
		requireLock := set.Bool("lock", false, "require a Composer lock file with --missing")
		pos, err := parseFlags(set, args[1:])
		if err != nil {
			return err
		}
		if *missing {
			if len(pos) != 0 || *checksum != "" || *zend {
				return errors.New("usage: phpm ext install --missing [--no-dev] [--lock]; do not combine with a name, --sha256 or --zend")
			}
			root, err := findProject(".")
			if err != nil {
				return err
			}
			fmt.Fprintln(c.Out, "Inspecting Composer requirements for the current project PHP...")
			provider := windowsExtensionProvider{Store: c.Store, Client: officialClient(), Progress: c.Out}
			env, versions, report, err := c.Store.installMissingExtensions(root, *noDev, *requireLock, provider)
			for _, warning := range report.Warnings {
				fmt.Fprintln(c.Out, "Note:", warning)
			}
			if err != nil {
				return err
			}
			if len(versions) == 0 {
				fmt.Fprintln(c.Out, "All inspected Composer requirements are already satisfied. No project files changed.")
				return nil
			}
			names := make([]string, 0, len(versions))
			for name := range versions {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(c.Out, "Installed: %s@%s\n", name, versions[name])
			}
			fmt.Fprintf(c.Out, "Environment ready: %s\nUpdated phpm.yaml and phpm.lock. Composer files unchanged.\n", env.ID)
			return nil
		}
		if *noDev || *requireLock {
			return errors.New("--no-dev and --lock require --missing")
		}
		if len(pos) != 1 {
			return errors.New("usage: phpm ext install NAME[@VERSION] [--sha256 HASH] [--zend]")
		}
		name, version, _ := strings.Cut(pos[0], "@")
		if strings.HasSuffix(pos[0], "@") {
			return errors.New("extension version cannot be empty after @")
		}
		request := ExtensionRequest{Name: name, Version: version, SHA256: *checksum, Zend: *zend}
		if err := request.validate(); err != nil {
			return err
		}
		root, err := findProject(".")
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Resolving %s for the project PHP...\n", pos[0])
		provider := windowsExtensionProvider{Store: c.Store, Client: officialClient(), Progress: c.Out}
		env, installedVersion, err := c.Store.installProjectExtension(root, request, provider.acquire)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Installed: %s@%s\nEnvironment ready: %s\nUpdated phpm.yaml and phpm.lock.\n", name, installedVersion, env.ID)
		return nil
	}
	if args[0] == "list" {
		if len(args) != 1 {
			return errors.New("ext list does not accept arguments")
		}
		exts, err := c.Store.extensions()
		if err != nil {
			return err
		}
		for _, ext := range exts {
			fmt.Fprintf(c.Out, "%s  runtime=%s  zend=%v\n", ext.ID, ext.RuntimeID, ext.Zend)
		}
		if len(exts) == 0 {
			fmt.Fprintln(c.Out, "No extensions imported.")
		}
		return nil
	}
	if args[0] != "import" {
		return fmt.Errorf("unknown ext command %q; use ext list, install, import or check", args[0])
	}
	set := flag.NewFlagSet("ext import", flag.ContinueOnError)
	file := set.String("file", "", "local DLL/SO")
	runtimeID := set.String("runtime", "", "target runtime ID")
	packageDir := set.String("package-dir", "", "include dependency files from this directory")
	checksum := set.String("sha256", "", "expected module checksum")
	zend := set.Bool("zend", false, "load as Zend extension")
	pos, err := parseFlags(set, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || *file == "" {
		return errors.New("usage: phpm ext import NAME@VERSION --file FILE [--runtime ID] [--zend]")
	}
	name, version, ok := strings.Cut(pos[0], "@")
	if !ok {
		return errors.New("extension requires NAME@VERSION")
	}
	if *checksum != "" && !validSHA(*checksum) {
		return errors.New("invalid --sha256")
	}
	var rt Runtime
	if *runtimeID != "" {
		rt, err = c.Store.readRuntime(*runtimeID)
	} else {
		var root string
		root, err = findProject(".")
		if err == nil {
			var cfg Config
			cfg, err = readConfig(root)
			if err == nil {
				// An existing matching lock pins the target build even if a
				// newer patch has since been imported into the shared store.
				lock, lockErr := loadLock(root)
				if lockErr == nil {
					if pinned, ok := lock.Targets[platformKey()]; ok && pinned.ConfigHash == digestJSON(cfg) {
						rt, err = c.Store.readRuntime(pinned.Runtime.ID)
					} else {
						rt, err = c.Store.resolveRuntime(cfg)
					}
				} else if errors.Is(lockErr, os.ErrNotExist) {
					rt, err = c.Store.resolveRuntime(cfg)
				} else {
					err = lockErr
				}
			}
		}
	}
	if err != nil {
		return err
	}
	ext, err := c.Store.importExtension(rt, name, version, *file, *packageDir, *checksum, *zend)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Imported:", ext.ID)
	fmt.Fprintf(c.Out, "Declare extensions.%s: %q in phpm.yaml, then run phpm install.\n", ext.Name, ext.Version)
	return nil
}

func (c *CLI) execute(args []string) (int, error) {
	candidate := len(args) > 0 && args[0] == "--candidate"
	if candidate {
		args = args[1:]
	}
	if len(args) == 0 || args[0] != "--" || len(args) < 2 {
		return 0, errors.New("usage: phpm exec [--candidate] -- php [ARGUMENTS...]")
	}
	args = args[1:]
	root, err := findProject(".")
	if err != nil {
		return 0, err
	}
	var env LockedEnvironment
	if candidate {
		var plan UpgradePlan
		plan, err = c.Store.candidate(root)
		env = plan.Environment
	} else {
		env, err = c.Store.current(root)
	}
	if err != nil {
		return 0, err
	}
	if args[0] == "composer" || args[0] == "composer.phar" {
		return c.runComposer(env, args[1:])
	}
	binary := c.Store.runtimeBinary(env.Runtime)
	commandArgs := args[1:]
	if args[0] == "php" || args[0] == "php.exe" {
		commandArgs = append([]string{"-c", filepath.Join(c.Store.environmentPath(env), "php.ini")}, commandArgs...)
	} else {
		binary, err = exec.LookPath(args[0])
		if err != nil {
			return 0, err
		}
		binary, err = filepath.Abs(binary)
		if err != nil {
			return 0, err
		}
	}
	return execProcess(binary, commandArgs, c.Store.environmentVars(env, c.Store.environmentPath(env)), c.In, c.Out, c.Err)
}
