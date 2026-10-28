//go:build windows

package phpm

import (
	"errors"
	"io"
	"os"
	"os/exec"
)

// Go uses MoveFileExW with REPLACE_EXISTING, without deleting the old file first.
func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func execProcess(binary string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, stdin, stdout, stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
