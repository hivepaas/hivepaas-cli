// Package carry turns what the API answers about an object into the request
// that writes it back: a command changes one thing - an image, a variable - and
// sends everything else as it was. The two shapes differ, and the server
// replaces the object with what the request holds, so a field left behind here
// is a setting the command would erase. carry_test.go fails on any field of the
// request these conversions do not fill.
package carry

import (
	"encoding/json"
	"fmt"

	"github.com/hivepaas/hivepaas-cli/internal/api"
)

// DeploymentSettings is the request that writes an app's deployment settings
// back as they are.
func DeploymentSettings(settings *api.AppsettingsdtoDeploymentSettingsResp) (
	*api.AppsettingsdtoUpdateAppDeploymentSettingsReq, error,
) {
	var req api.AppsettingsdtoUpdateAppDeploymentSettingsReq
	if err := convert(settings, &req); err != nil {
		return nil, fmt.Errorf("the deployment settings: %w", err)
	}
	return &req, nil
}

// EnvVars is the request that writes an app's environment variables back as
// they are: its own, without those HivePaaS sets (isSystem), which the lists
// show beside them and the server does not take back.
func EnvVars(vars *api.AppsettingsdtoEnvVarsResp) (*api.AppsettingsdtoUpdateAppEnvVarsReq, error) {
	own := *vars
	own.RuntimeEnvVars = withoutSystem(vars.RuntimeEnvVars)
	own.BuildtimeEnvVars = withoutSystem(vars.BuildtimeEnvVars)
	own.SharedEnvVars = withoutSystem(vars.SharedEnvVars)
	var req api.AppsettingsdtoUpdateAppEnvVarsReq
	if err := convert(&own, &req); err != nil {
		return nil, fmt.Errorf("the environment variables: %w", err)
	}
	return &req, nil
}

func withoutSystem(list *[]api.BasedtoEnvVarResp) *[]api.BasedtoEnvVarResp {
	out := []api.BasedtoEnvVarResp{}
	if list == nil {
		return &out
	}
	for _, v := range *list {
		if v.IsSystem == nil || !*v.IsSystem {
			out = append(out, v)
		}
	}
	return &out
}

// convert copies from into to through JSON. The fields have the same names on
// both sides, and an object the response gives whole - a registry auth - fills
// the request's {id} with its id.
func convert(from, to any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err //nolint:wrapcheck // the callers say what failed
	}
	return json.Unmarshal(data, to) //nolint:wrapcheck
}
