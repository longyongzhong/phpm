package phpm

import (
	"errors"
	"fmt"
	"strings"

	"phpm/internal/catalog"
)

// The public root is part of the reviewed client, never fetched and trusted from
// the download server. Publishing private keys are not included in the client.
const defaultSourceName = "phpm"
const defaultCatalogURL = "https://github.com/longyongzhong/phpm/releases/download/v0.1.0-rc.2/catalog.json"
const defaultRootPublicKey = "C6HfDYZeDaN87XjsxcB0Xe+IDRCeDiEWgLzAIHl9Dd8="
const defaultRootFingerprint = "b9aaa6269e68f95ea18147d09c043ca9c8ae332903e0bc04b23c34ad98e35d32"
const defaultCatalogSequence uint64 = 40999599815

func builtinSource() catalogSource {
	return catalogSource{Schema: 1, Name: defaultSourceName, Location: defaultCatalogURL,
		PublicKey: defaultRootPublicKey, TrustMode: catalog.RootMode, Sequence: defaultCatalogSequence}
}

// sourceSelection is shared by listing, installation and upgrade acquisition.
// There is no fallback from a signed source to a community distribution.
func sourceSelection(goos, arch, source, provider string) (string, bool, error) {
	if source != "" && provider != "" {
		return "", false, errors.New("--source and --provider cannot be combined")
	}
	if provider != "" {
		if provider != "staticphp" {
			return "", false, errors.New("unknown provider; the explicit community provider is --provider staticphp")
		}
		if goos != "linux" || arch != "amd64" {
			return "", false, errors.New("the StaticPHP provider requires Linux x86_64")
		}
		return "", true, nil
	}
	if source != "" {
		return source, false, nil
	}
	switch goos {
	case "windows":
		return "", false, nil // The existing official Windows provider.
	case "linux":
		if arch == "amd64" {
			return defaultSourceName, false, nil
		}
	}
	return "", false, errors.New("automatic PHP downloads support Windows x64 and signed Debian 12/13 x64; use php import on this platform")
}

func validateBuiltinPin(source, expected catalogSource) error {
	if source.Name != expected.Name || source.PublicKey != expected.PublicKey || source.TrustMode != catalog.RootMode {
		return fmt.Errorf("source %s conflicts with this client's pinned built-in root; use a different name for custom sources", expected.Name)
	}
	// Permit a newer client to move between PHPM's immutable release catalogs,
	// keeping existing authorization/revocation/sequence history intact.
	prefix := "https://github.com/longyongzhong/phpm/releases/download/"
	if source.Location != expected.Location && (!strings.HasPrefix(source.Location, prefix) || !strings.HasSuffix(source.Location, "/catalog.json")) {
		return errors.New("built-in PHPM source location was changed; use --source with a separately named source")
	}
	return nil
}
