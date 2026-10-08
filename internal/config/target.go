package config

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotLoggedIn is a command run with no installation to talk to.
var ErrNotLoggedIn = errors.New("not logged in: run `hivepaas login <url>`, " +
	"or set HIVEPAAS_URL and HIVEPAAS_API_KEY")

// Target is the installation a command talks to, and the key it uses.
type Target struct {
	// Context is the context's name, or "" for one given by the environment.
	Context string
	URL     string
	KeyID   string
	Secret  string
}

// ResolveTarget finds the installation to use: HIVEPAAS_URL and
// HIVEPAAS_API_KEY when set - CI, where nothing is stored - else the context
// named by the flag, by HIVEPAAS_CONTEXT, or the current one.
func ResolveTarget(cfg *Config, secrets *Secrets, contextFlag string, getenv func(string) string) (*Target, error) {
	if key := getenv("HIVEPAAS_API_KEY"); key != "" {
		url := getenv("HIVEPAAS_URL")
		if url == "" {
			return nil, errors.New("HIVEPAAS_API_KEY is set, and HIVEPAAS_URL is not") //nolint:err113
		}
		keyID, secret, found := strings.Cut(key, ":")
		if !found || keyID == "" || secret == "" {
			return nil, errors.New("HIVEPAAS_API_KEY is not <key id>:<secret>") //nolint:err113
		}
		return &Target{URL: NormalizeURL(url), KeyID: keyID, Secret: secret}, nil
	}

	name := contextFlag
	if name == "" {
		name = getenv("HIVEPAAS_CONTEXT")
	}
	if name == "" {
		name = cfg.Current
	}
	if name == "" {
		return nil, ErrNotLoggedIn
	}
	ctx := cfg.Contexts[name]
	if ctx == nil {
		return nil, fmt.Errorf("there is no context %q: `hivepaas context ls` lists them", name) //nolint:err113
	}
	secret, err := secrets.Get(ctx.URL, ctx.KeyID)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, fmt.Errorf("the key of context %q has no secret here: log in again", name) //nolint:err113
	}
	return &Target{Context: name, URL: ctx.URL, KeyID: ctx.KeyID, Secret: secret}, nil
}

// NormalizeURL is an installation's URL as the CLI keeps it: with a scheme, no
// trailing slash, and no /api - the CLI adds the API's path itself.
func NormalizeURL(url string) string {
	url = strings.TrimSpace(url)
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}
	url = strings.TrimRight(url, "/")
	return strings.TrimSuffix(url, "/api")
}
