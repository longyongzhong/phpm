package phpm

import (
	"encoding/json"
	"errors"
	"flag"
)

func (c *CLI) checkComposerExtensions(args []string) (int, error) {
	set := flag.NewFlagSet("ext check", flag.ContinueOnError)
	noDev := set.Bool("no-dev", false, "exclude root and locked development requirements")
	candidate := set.Bool("candidate", false, "check the prepared PHP upgrade")
	asJSON := set.Bool("json", false, "machine-readable output")
	requireLock := set.Bool("lock", false, "require a Composer lock file")
	pos, err := parseFlags(set, args)
	if err != nil {
		return 0, err
	}
	if len(pos) != 0 {
		return 0, errors.New("usage: phpm ext check [--no-dev] [--candidate] [--json] [--lock]")
	}
	root, err := findProject(".")
	if err != nil {
		return 0, err
	}
	env, err := c.projectEnvironment(root, *candidate)
	if err != nil {
		return 0, err
	}
	report, err := c.Store.checkComposer(root, env, *noDev, *requireLock)
	if err != nil {
		return 0, err
	}
	if *asJSON {
		encoder := json.NewEncoder(c.Out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return 0, err
		}
	} else {
		writeComposerCheck(c.Out, report)
	}
	return composerCheckCode(report), nil
}
