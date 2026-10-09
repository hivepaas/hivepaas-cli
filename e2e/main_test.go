//go:build e2e

// Package e2e runs the CLI's binary against a throwaway installation - the
// dashboard's e2e/env/up.sh - as a CI job runs it, and looks at what it did.
// docs/superpowers/specs/2026-10-09-cli-e2e-design.md says what and how.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/config"
)

// env is where and as whom the tests run: the dashboard's tests' names for
// the same things, and its throwaway installation's addresses by default.
var env = struct {
	baseURL, username, password, ingressHTTP, ingressHTTPS string
}{
	baseURL:      getenv("HP_E2E_BASE_URL", "http://localhost:10100"),
	username:     getenv("HP_E2E_USERNAME", "admin"),
	password:     getenv("HP_E2E_PASSWORD", "abc123"),
	ingressHTTP:  getenv("HP_E2E_INGRESS_HTTP", "127.0.0.1:10180"),
	ingressHTTPS: getenv("HP_E2E_INGRESS_HTTPS", "127.0.0.1:10443"),
}

var (
	// binary is the CLI, built for the run.
	binary string
	// apiKey is the run's key, KEYID:SECRET, as HIVEPAAS_API_KEY takes it.
	apiKey string
	// runID names what the run makes.
	runID = strconv.FormatInt(time.Now().Unix()%1_000_000, 36)
	// hp is the API as the run's key, for what a test makes besides the CLI.
	hp *client.Client
)

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func TestMain(m *testing.M) {
	os.Exit(runAll(m))
}

func runAll(m *testing.M) int {
	if isLocalBackend(env.baseURL) {
		fmt.Fprintf(os.Stderr, "%s is the local backend, a developer's own data: these tests make, deploy and "+
			"remove things. Run them on the throwaway installation - the dashboard's e2e/env/up.sh - with "+
			"HP_E2E_BASE_URL=http://localhost:10100.\n", env.baseURL)
		return 1
	}
	dir, err := os.MkdirTemp("", "hivepaas-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binary = filepath.Join(dir, "hivepaas")
	build := exec.Command("go", "build", "-o", binary, "github.com/hivepaas/hivepaas-cli/cmd/hivepaas")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err = build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the CLI:", err)
		return 1
	}
	ctx := context.Background()
	keyID, secret, deleteKey, err := makeKey(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := deleteKey(); err != nil {
			fmt.Fprintln(os.Stderr, "deleting the run's API key:", err)
		}
	}()
	apiKey = keyID + ":" + secret
	if hp, err = client.New(&config.Target{URL: env.baseURL, KeyID: keyID, Secret: secret},
		client.Options{Timeout: 2 * time.Minute}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

// isLocalBackend is whether the address is the local backend's: `make
// local-app-run`, a developer's own data on their own swarm.
func isLocalBackend(address string) bool {
	u, err := url.Parse(address)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return u.Port() == "10000"
	}
	return false
}

// makeKey signs in with the password and makes the run's API key: every
// action, expiring tomorrow. deleteKey signs in again - the session may have
// expired meanwhile - and deletes it.
func makeKey(ctx context.Context) (keyID, secret string, deleteKey func() error, err error) {
	token, err := signIn(ctx)
	if err != nil {
		return "", "", nil, err
	}
	var made struct {
		Data struct {
			ID        string `json:"id"`
			KeyID     string `json:"keyId"`
			SecretKey string `json:"secretKey"`
		} `json:"data"`
	}
	err = asUser(ctx, token, http.MethodPost, "users/current/settings/api-keys", map[string]any{
		"name":         "cli-e2e-" + runID,
		"expireAt":     time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"accessAction": map[string]bool{"read": true, "write": true, "delete": true, "execute": true},
		"capabilities": []string{},
	}, &made)
	if err != nil {
		return "", "", nil, fmt.Errorf("making the run's API key: %w", err)
	}
	deleteKey = func() error {
		token, err := signIn(context.Background())
		if err != nil {
			return err
		}
		return asUser(context.Background(), token, http.MethodDelete,
			"users/current/settings/api-keys/"+made.Data.ID, nil, nil)
	}
	return made.Data.KeyID, made.Data.SecretKey, deleteKey, nil
}

func signIn(ctx context.Context) (string, error) {
	var session struct {
		Data struct {
			Session *struct {
				AccessToken string `json:"accessToken"`
			} `json:"session"`
		} `json:"data"`
	}
	err := asUser(ctx, "", http.MethodPost, "auth/login-with-password",
		map[string]string{"username": env.username, "password": env.password}, &session)
	if err != nil {
		return "", fmt.Errorf("signing in to %s as %s: %w", env.baseURL, env.username, err)
	}
	if session.Data.Session == nil {
		return "", fmt.Errorf("signing in to %s as %s: no session - the account asks for more", env.baseURL,
			env.username)
	}
	return session.Data.Session.AccessToken, nil
}

// asUser calls the API with a session's token, as the dashboard does.
func asUser(ctx context.Context, token, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, env.baseURL+"/api/"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
