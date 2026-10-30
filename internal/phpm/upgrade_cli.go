package phpm

import (
	"errors"
	"flag"
	"fmt"
	"sort"
)

func (c *CLI) preparePHPUpgrade(args []string) error {
	set := flag.NewFlagSet("php upgrade", flag.ContinueOnError)
	runtimeID := set.String("runtime", "", "use a locally imported target build")
	source := set.String("source", "", "use a registered signed catalog")
	providerName := set.String("provider", "", "explicit community provider: staticphp")
	pos, err := parseFlags(set, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: phpm php upgrade [VERSION] [--runtime ID | --source NAME | --provider staticphp]")
	}
	if (*source != "" && *runtimeID != "") || (*providerName != "" && (*source != "" || *runtimeID != "")) {
		return errors.New("--source, --runtime and --provider cannot be combined")
	}
	if *providerName != "" && *providerName != "staticphp" {
		return errors.New("unknown provider; use --provider staticphp")
	}
	request := runtimeUpgradeRequest{RuntimeID: *runtimeID, Source: *source, Provider: *providerName}
	if len(pos) == 1 {
		request.Version = pos[0]
	}
	root, err := findProject(".")
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Preparing PHP and matching project extensions; current project files stay unchanged...")
	provider := windowsExtensionProvider{Store: c.Store, Client: officialClient(), Progress: c.Out}
	plan, err := c.Store.prepareUpgrade(root, request, c.Store.acquireUpgradeRuntime, provider.acquire)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Candidate ready: %s\nPHP: %s\nReview: phpm diff\nTest: phpm exec --candidate -- php -v\nSwitch after testing: phpm activate %s\n", plan.ID, plan.Environment.Runtime.PHP.Version, plan.ID)
	return nil
}

func (c *CLI) upgradeCommand(args []string) error {
	root, err := findProject(".")
	if err != nil {
		return err
	}
	switch args[0] {
	case "activate":
		if len(args) != 2 {
			return errors.New("usage: phpm activate CANDIDATE_ID (review phpm diff and test the candidate first)")
		}
		plan, err := c.Store.activateUpgrade(root, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Project now selects PHP %s (%s).\nExisting processes are unchanged. Restart/reload your application separately and check its health.\n", plan.Environment.Runtime.PHP.Version, plan.Environment.ID)
		return nil
	case "rollback":
		if len(args) > 2 {
			return errors.New("usage: phpm rollback [CHECKPOINT_ID]")
		}
		id := ""
		if len(args) == 2 {
			id = args[1]
		}
		env, err := c.Store.rollbackUpgrade(root, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Restored project files and PHP %s (%s).\nExisting processes are unchanged. Restart/reload your application separately and check its health.\n", env.Runtime.PHP.Version, env.ID)
		return nil
	case "history":
		if len(args) != 1 {
			return errors.New("history does not accept arguments")
		}
		plans, err := c.Store.upgradeHistory(root)
		if err != nil {
			return err
		}
		if len(plans) == 0 {
			fmt.Fprintln(c.Out, "No retained upgrade restore points.")
			return nil
		}
		for _, plan := range plans {
			before, err := decodeLock(plan.BeforeLock.Data)
			if err != nil {
				return err
			}
			status := "retained"
			if projectHasBytes(root, plan.AfterConfig, plan.AfterLock) {
				status = "matches current project"
			}
			fmt.Fprintf(c.Out, "%s  %s  PHP %s -> %s  [%s]\n", plan.ID, plan.CreatedAt.Local().Format("2006-01-02T15:04:05-07:00"), before.Targets[platformKey()].Runtime.PHP.Version, plan.Environment.Runtime.PHP.Version, status)
		}
		return nil
	case "diff":
		if len(args) != 1 {
			return errors.New("diff does not accept arguments")
		}
		plan, err := c.Store.candidate(root)
		if err != nil {
			return err
		}
		before, err := decodeLock(plan.BeforeLock.Data)
		if err != nil {
			return err
		}
		old := before.Targets[platformKey()]
		fmt.Fprintf(c.Out, "Candidate: %s\nPHP: %s -> %s\nBuild: %s -> %s\n", plan.ID, old.Runtime.PHP.Version, plan.Environment.Runtime.PHP.Version, old.Runtime.ID, plan.Environment.Runtime.ID)
		oldCfg, err := decodeConfig(plan.BeforeConfig.Data)
		if err != nil {
			return err
		}
		newCfg, err := decodeConfig(plan.AfterConfig)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(newCfg.Extensions))
		for name := range newCfg.Extensions {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(c.Out, "Extension %s: %s -> %s (target PHP binding)\n", name, oldCfg.Extensions[name], newCfg.Extensions[name])
		}
		fmt.Fprintln(c.Out, "INI settings preserved. Project files switch only with activate; existing processes require a separate restart.")
		return nil
	}
	return errors.New("unknown upgrade command")
}
