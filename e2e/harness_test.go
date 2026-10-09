//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// commandTimeout is the longest one command may take: a deployment that pulls
// an image into the installation included.
const commandTimeout = 10 * time.Minute

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\n--- stdout\n%s--- stderr\n%s", r.code, r.stdout, r.stderr)
}

// cli runs the CLI as a CI job does: the run's key in its environment, a
// config directory of its own, no terminal - in dir, stdin piped in.
type cli struct {
	t     *testing.T
	dir   string
	stdin string
	// flags go after the arguments of each command: the project, env and app.
	flags []string
}

// in is the same CLI run in dir.
func (c cli) in(dir string) cli {
	c.dir = dir
	return c
}

// with is the same CLI given stdin.
func (c cli) with(stdin string) cli {
	c.stdin = stdin
	return c
}

// run runs the CLI with args - and the flags - and answers what it did.
func (c cli) run(args ...string) result {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append(args, c.flags...)...)
	cmd.Dir = c.dir
	if cmd.Dir == "" {
		cmd.Dir = c.t.TempDir()
	}
	cmd.Env = c.environ()
	cmd.Stdin = strings.NewReader(c.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		r.code = exitErr.ExitCode()
	case err != nil:
		c.t.Fatalf("running hivepaas %s: %v", strings.Join(args, " "), err)
	}
	c.t.Logf("hivepaas %s: exit %d", strings.Join(args, " "), r.code)
	return r
}

// interrupted runs the CLI as run does, and sends it Ctrl-C - SIGINT - after
// the time given: what a command that goes on until stopped is ended by.
func (c cli) interrupted(after time.Duration, args ...string) result {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), after)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append(args, c.flags...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	cmd.Dir = c.dir
	if cmd.Dir == "" {
		cmd.Dir = c.t.TempDir()
	}
	cmd.Env = c.environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		r.code = exitErr.ExitCode()
	case err != nil:
		c.t.Fatalf("running hivepaas %s: %v", strings.Join(args, " "), err)
	}
	c.t.Logf("hivepaas %s, interrupted after %s: exit %d", strings.Join(args, " "), after, r.code)
	return r
}

// environ is the environment the CLI runs in: the run's key, a config
// directory of its own, nothing else of the person's that selects or asks.
func (c cli) environ() []string {
	return append(os.Environ(), "HIVEPAAS_URL="+env.baseURL, "HIVEPAAS_API_KEY="+apiKey,
		"HIVEPAAS_CONFIG_DIR="+c.t.TempDir(), "HIVEPAAS_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1",
		"HIVEPAAS_PROJECT=", "HIVEPAAS_ENV=", "HIVEPAAS_APP=", "HIVEPAAS_CONTEXT=")
}

// must runs the CLI as run does, and fails the test unless it exits 0.
func (c cli) must(args ...string) result {
	c.t.Helper()
	r := c.run(args...)
	require.Zero(c.t, r.code, "hivepaas %s\n%s", strings.Join(args, " "), r)
	return r
}

// project makes a project of the test's, with a production environment, and
// deletes it with its storage when the test ends. The CLI it answers runs in
// that environment.
func project(t *testing.T, name string) cli {
	t.Helper()
	name = "e2e-" + name + "-" + runID
	c := cli{t: t}
	c.must("project", "create", name, "--envs", "production")
	t.Cleanup(func() {
		if r := c.run("project", "delete", name, "--yes", "--remove-storage"); r.code != 0 {
			t.Logf("deleting project %s:\n%s", name, r)
		}
	})
	c.flags = []string{"-p", name, "-e", "production"}
	return c
}

// app is the CLI with an app of the environment selected as well.
func (c cli) app(name string) cli {
	c.flags = append(append([]string{}, c.flags...), "-a", name)
	return c
}

// projectName is the project the CLI's flags select.
func (c cli) projectName() string {
	for i, flag := range c.flags {
		if flag == "-p" {
			return c.flags[i+1]
		}
	}
	return ""
}

// web reaches the apps' domains as a browser does, through the installation's
// proxy: it dials the ingress whatever the host, and takes its certificates.
var web = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ingress := env.ingressHTTP
			if strings.HasSuffix(addr, ":443") {
				ingress = env.ingressHTTPS
			}
			return (&net.Dialer{}).DialContext(ctx, network, ingress)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the throwaway installation's own
	},
}

// get asks the url, and answers its status and body.
func get(address string) (int, string, error) {
	resp, err := web.Get(address) //nolint:noctx // the client's timeout bounds it
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// eventually calls check until it answers nil, up to within; the test fails
// with the last thing check said.
func eventually(t *testing.T, within time.Duration, check func() error) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %s: %v", within, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// call calls the API as the run's key - for what a test makes besides the
// CLI - and decodes the answer into out.
func call(t *testing.T, method, path string, body, out any) {
	t.Helper()
	require.NoError(t, tryCall(method, path, body, out))
}

func tryCall(method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := hp.Raw(ctx, method, path, reader)
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

// files writes files into a new directory, and answers it.
func files(t *testing.T, contents map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range contents {
		full := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}
	return dir
}

// jsonOf decodes a command's -o json output.
func jsonOf[T any](t *testing.T, r result) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &v), r.String())
	return v
}

// appPath is the API's path of the app the CLI selects.
func (c cli) appPath() string {
	c.t.Helper()
	project := jsonOf[struct {
		ID string `json:"id"`
	}](c.t, c.must("project", "get", "-o", "json"))
	app := jsonOf[struct {
		ID string `json:"id"`
	}](c.t, c.must("app", "get", "-o", "json"))
	require.NotEmpty(c.t, project.ID)
	require.NotEmpty(c.t, app.ID)
	return "projects/" + project.ID + "/production/apps/" + app.ID
}
