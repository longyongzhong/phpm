package phpm

import (
	"errors"
	"io"
	"runtime"
)

func (s *Store) installRuntime(requested string, ts bool, progress io.Writer) (Runtime, error) {
	switch runtime.GOOS {
	case "windows":
		return s.installWindows(requested, ts)
	case "linux":
		if runtime.GOARCH != "amd64" {
			return Runtime{}, errors.New("automatic Linux PHP downloads currently support x86_64; use php import for other architectures")
		}
		if ts {
			return Runtime{}, errors.New("the Linux StaticPHP provider supplies NTS builds only; use php import for TS builds")
		}
		return (linuxRuntimeProvider{Store: s, Client: staticPHPClient(), Progress: progress}).install(requested)
	default:
		return Runtime{}, errors.New("automatic PHP downloads support Windows x64 and Linux x64; use php import on this platform")
	}
}
