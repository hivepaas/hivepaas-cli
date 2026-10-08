package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const routing = `{"data":{"port":8080,"exposePublicly":false,"domainSuggestion":"<name>.example.com","updateVer":2,
	"domains":[{"domain":"api.example.com","containerPort":3000,"enabled":true,"protocol":"http","forceHttps":true,
		"sslCert":{"id":"CERT1","name":"wildcard"},"headerConfig":{"enabled":true}}]}}`

func TestDomainAddCarriesTheOthers(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/routing-settings", http.StatusOK, routing)
	f.json("PUT "+appPath+"/routing-settings", http.StatusOK, `{"meta":null}`)

	r := f.run(args("domain add Admin.Example.com " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Added admin.example.com, port 3000, and exposed the app publicly on api")
	written := f.body("PUT "+appPath+"/routing-settings", 0)
	assert.Equal(t, true, written["exposePublicly"], "a domain is served only from an app exposed publicly")
	assert.InDelta(t, 2, written["updateVer"], 0)
	domains, _ := written["domains"].([]any)
	require.Len(t, domains, 2)
	first, _ := domains[0].(map[string]any)
	assert.Equal(t, map[string]any{"id": "CERT1"}, first["sslCert"], "the other domain as it was, its certificate's id")
	header, _ := first["headerConfig"].(map[string]any)
	assert.Equal(t, true, header["enabled"], "its headers' settings carried too")
	added, _ := domains[1].(map[string]any)
	assert.Equal(t, "admin.example.com", added["domain"])
	assert.InDelta(t, 3000, added["containerPort"], 0, "the first domain's port")
	assert.Equal(t, true, added["forceHttps"])

	r = f.run(args("domain add api.example.com " + shopAPI)...)
	assert.Equal(t, exitcode.Invalid, r.code, "a domain it has already")
	r = f.run(args("domain rm nope.example.com " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Contains(t, r.stderr, "is not a domain of the app: api.example.com")
	assert.Equal(t, 1, f.called("PUT "+appPath+"/routing-settings"), "nothing written for either")
}

func TestDomainLsSuggests(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/routing-settings", http.StatusOK,
		`{"data":{"port":8080,"exposePublicly":false,"domainSuggestion":"<name>.example.com","domains":[]}}`)

	r := f.run(args("domain ls " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "No domain yet: hivepaas domain add api.example.com -p shop -e production -a api",
		"the pattern filled with the app's key, to be run as it is")
}

func TestScale(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/autoscale", http.StatusOK,
		`{"data":{"enabled":true,"minReplicas":2,"maxReplicas":6,"cpuTarget":70,"target":80,"scaleInDelay":"5m",`+
			`"replicas":3,"updateVer":4}}`)
	f.json("PUT "+appPath+"/autoscale", http.StatusOK, `{"meta":null}`)
	f.json("GET "+appPath+"/service-settings", http.StatusOK,
		`{"data":{"modeSpec":{"mode":"replicated","serviceReplicas":3},`+
			`"placement":{"constraints":[{"name":"node.role","op":"==","value":"worker"}]},`+
			`"updateVer":9}}`)
	f.json("PUT "+appPath+"/service-settings", http.StatusOK, `{"meta":null}`)
	f.json("GET "+appPath+"/resource-settings", http.StatusOK,
		`{"data":{"limits":{"memory":"256MB","pids":100},"reservations":{"cpus":0.1},"updateVer":1}}`)
	f.json("PUT "+appPath+"/resource-settings", http.StatusOK, `{"meta":null}`)

	r := f.run(args("app scale --replicas 2 " + shopAPI)...)
	assert.Equal(t, exitcode.Invalid, r.code, "autoscale sets the replicas")
	assert.Zero(t, f.called("PUT "+appPath+"/service-settings"))

	r = f.run(args("app scale --no-autoscale --replicas 2 --cpus 0.5 " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Autoscale off; replicas 2; limits CPUs 0.5 on api (shop / production).")
	autoscale := f.body("PUT "+appPath+"/autoscale", 0)
	assert.Equal(t, false, autoscale["enabled"])
	assert.InDelta(t, 6, autoscale["maxReplicas"], 0, "the rest as it was")
	assert.Equal(t, "5m", autoscale["scaleInDelay"])
	service := f.body("PUT "+appPath+"/service-settings", 0)
	assert.Equal(t, map[string]any{"mode": "replicated", "serviceReplicas": float64(2)}, service["modeSpec"])
	placement, _ := service["placement"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"name": "node.role", "op": "==", "value": "worker"}}, placement["constraints"],
		"the placement as it was")
	resources := f.body("PUT "+appPath+"/resource-settings", 0)
	assert.Equal(t, map[string]any{"cpus": 0.5, "memory": "256MB", "pids": float64(100)}, resources["limits"])
	assert.Equal(t, map[string]any{"cpus": 0.1}, resources["reservations"])

	r = f.run(args("app scale " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "api (shop / production)\nReplicas:  3 now, autoscaled between 2 and 6, on CPU at 70%\n"+
		"Limits:    memory 256MB, each\n", r.stdout)
}
