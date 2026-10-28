//go:build !windows

package phpm

import (
	"io"
	"os"
	"syscall"
)

func replaceFile(source, target string) error { return os.Rename(source, target) }

// Replace the launcher so signals, process identity and exit status belong to
// the application, including when launched by systemd or Supervisor.
func execProcess(binary string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return 0, syscall.Exec(binary, append([]string{binary}, args...), env)
}
