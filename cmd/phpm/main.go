package main

import (
	"os"

	"phpm/internal/phpm"
)

func main() {
	os.Exit(phpm.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
