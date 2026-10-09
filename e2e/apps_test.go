//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// busybox is the image the apps here run: small, with a shell and a web server.
const busybox = "busybox:1.37"

// deployWithin is how long an app takes to answer once deployed or changed.
const deployWithin = 3 * time.Minute

// An app made from an image, deployed, reached at its domain, changed, copied
// into, scaled, stopped and started - each looked at where it shows.
func TestAnAppFromAnImage(t *testing.T) {
	t.Parallel()
	p := project(t, "image")
	web := p.app("web")
	serve := `sh -c 'echo "ready-$GREETING"; mkdir -p /www && echo "hello-$GREETING" > /www/index.html && ` +
		`exec httpd -f -p 80 -h /www'`

	r := p.must("app", "create", "web", "--image", busybox, "--command", serve, "--port", "80",
		"--var", "GREETING=e2e")
	assert.Contains(t, r.stderr, "Deployed")
	// A deployment is done once swarm has the service; its replica runs a
	// little after.
	eventually(t, deployWithin, func() error { return running(web, 1) })
	assert.Contains(t, web.must("env", "ls").stdout, "GREETING")
	eventually(t, time.Minute, func() error {
		return contains(web.run("logs", "--tail", "50").stdout, "ready-e2e")
	})

	domain := "web-" + runID + ".localhost"
	web.must("domain", "add", domain, "--no-force-https")
	assert.Contains(t, web.must("domain", "ls").stdout, domain)
	page := "http://" + domain + "/"
	eventually(t, deployWithin, func() error { return answers(page, "hello-e2e") })

	web.must("env", "set", "GREETING=changed")
	web.must("restart")
	eventually(t, deployWithin, func() error { return answers(page, "hello-changed") })

	// A file copied in is served; copied out, it is the same file.
	marker := "copied-" + runID
	dir := files(t, map[string]string{"index.html": marker + "\n"})
	web.in(dir).must("cp", "./index.html", ":/www/index.html")
	eventually(t, time.Minute, func() error { return answers(page, marker) })
	web.in(dir).must("cp", ":/www/index.html", "./back.html")
	back, err := os.ReadFile(filepath.Join(dir, "back.html"))
	require.NoError(t, err)
	assert.Equal(t, marker+"\n", string(back))

	r = web.with("cat /www/index.html\nexit\n").run("exec")
	if assert.Zero(t, r.code, r.String()) {
		assert.Contains(t, r.stdout, marker)
	}

	web.must("app", "scale", "--replicas", "2")
	eventually(t, deployWithin, func() error { return running(web, 2) })
	web.must("app", "stop")
	eventually(t, deployWithin, func() error { return running(web, 0) })
	web.must("app", "start")
	eventually(t, deployWithin, func() error { return running(web, 2) })

	app := jsonOf[map[string]any](t, web.must("app", "get", "-o", "json"))
	assert.Equal(t, "web", app["name"], "%v", app)
	assert.Contains(t, web.must("deploy", "ls").stdout, "done")
	assert.Contains(t, web.must("deploy", "settings").stdout, busybox)
	routing := jsonOf[map[string]any](t, web.must("api", "GET", "/projects/{project}/{env}/apps/{app}/routing-settings"))
	assert.Contains(t, fmt.Sprint(routing), domain)
}

// contains says what is missing when text does not hold want.
func contains(text, want string) error {
	if strings.Contains(text, want) {
		return nil
	}
	return fmt.Errorf("no %q in:\n%s", want, text)
}

// answers says what the address answered when it is not 200 with want.
func answers(address, want string) error {
	status, body, err := get(address)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s answered %d: %s", address, status, body)
	}
	return contains(body, want)
}

// running says how many replicas run when it is not n.
func running(c cli, n int) error {
	r := c.run("ps", "--state", "running")
	count := 0
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.Contains(line, "running") {
			count++
		}
	}
	if count != n {
		return fmt.Errorf("%d running, not %d:\n%s", count, n, r)
	}
	return nil
}
