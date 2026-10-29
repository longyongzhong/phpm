// phpm-release is an offline maintainer tool, not part of normal PHP execution.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"phpm/internal/catalog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "phpm-release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: phpm-release setup-github | keygen | trust | catalog | clients | verify (use COMMAND --help for flags)")
	}
	set := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "setup-github":
		return setupGitHubCommand(args[1:])
	case "trust":
		return trustCommand(args[1:])
	case "clients":
		return clientsCommand(args[1:])
	case "verify":
		return verifyCommand(args[1:])
	case "keygen":
		private := set.String("private", "", "new private seed file (base64, keep offline)")
		public := set.String("public", "", "new public key file (base64)")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		if *private == "" || *public == "" || *private == *public || set.NArg() != 0 {
			return errors.New("keygen requires different --private and --public paths")
		}
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*private, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(key.Seed()) + "\n")
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		f, err = os.OpenFile(*public, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			os.Remove(*private)
			return err
		}
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(pub) + "\n")
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		fmt.Println("Ed25519 public key SHA-256:", catalog.Fingerprint(pub))
		return nil
	case "catalog":
		keyFile := set.String("key", "", "offline private seed file")
		sequence := set.Uint64("sequence", 0, "monotonically increasing catalog sequence")
		output := set.String("output", "", "new release bundle directory")
		validity := set.Duration("validity", 30*24*time.Hour, "catalog lifetime, maximum 2160h")
		trustDir := set.String("trust", "", "root-signed authorization directory (delegated mode)")
		rootFile := set.String("root", "", "independently trusted root public key")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		if *keyFile == "" || *output == "" || set.NArg() == 0 {
			return errors.New("catalog requires --key, --sequence, --output and build directories")
		}
		f, err := os.Open(*keyFile)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, 1025))
		closeErr := f.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("invalid Ed25519 private seed file")
		}
		if (*trustDir == "") != (*rootFile == "") {
			return errors.New("--trust and --root must be used together")
		}
		if *trustDir != "" {
			root, err := readPublic(*rootFile)
			if err != nil {
				return err
			}
			if err := catalog.CreateDelegatedBundle(set.Args(), *output, seed, *sequence, time.Now(), *validity, *trustDir, root); err != nil {
				return err
			}
			fmt.Println("Root-authorized signed bundle ready:", *output)
			return nil
		}
		if err := catalog.CreateBundle(set.Args(), *output, seed, *sequence, time.Now(), *validity); err != nil {
			return err
		}
		fmt.Println("Signed bundle ready:", *output)
		return nil
	default:
		return errors.New("unknown release command")
	}
}
