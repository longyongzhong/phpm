package phpm

import (
	"context"
	"errors"
	"io"
	"runtime"
	"time"
)

func (s *Store) installRuntime(requested string, ts bool, progress io.Writer) (Runtime, error) {
	return s.installRuntimeFrom(requested, ts, "", "", progress)
}

func (s *Store) installRuntimeFrom(requested string, ts bool, source, provider string, progress io.Writer) (Runtime, error) {
	selected, static, err := sourceSelection(runtime.GOOS, runtime.GOARCH, source, provider)
	if err != nil {
		return Runtime{}, err
	}
	if selected != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return s.installCatalogRuntime(ctx, selected, requested, ts, progress)
	}
	if static {
		if ts {
			return Runtime{}, errors.New("the Linux StaticPHP provider supplies NTS builds only; use php import for TS builds")
		}
		return (linuxRuntimeProvider{Store: s, Client: staticPHPClient(), Progress: progress}).install(requested)
	}
	return s.installWindows(requested, ts)
}
