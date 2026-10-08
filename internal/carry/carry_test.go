package carry

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/api"
)

// notCarried are the fields of a request no response gives, each with why the
// server does not need it back. A field the server starts reading must leave
// this list.
var notCarried = map[string]string{
	"imageSource.enabled": "the server ignores it: activeMethod says which source deploys",
	"repoSource.enabled":  "the server ignores it: activeMethod says which source deploys",
}

// The deployment settings come back whole: every field of the request is filled
// from the response, so writing them back with one change changes nothing else.
func TestDeploymentSettingsCarryEveryField(t *testing.T) {
	var settings api.AppsettingsdtoDeploymentSettingsResp
	fill(reflect.ValueOf(&settings).Elem(), 0)

	req, err := DeploymentSettings(&settings)
	require.NoError(t, err)

	missing, used := unfilled(reflect.ValueOf(req).Elem(), "")
	assert.Empty(t, missing, "fields of the deployment settings request the CLI does not carry back from the "+
		"response: carry them in internal/carry, or say in notCarried why the server does not need them")
	assert.ElementsMatch(t, slices.Collect(maps.Keys(notCarried)), used,
		"notCarried names a field the request no longer has")
}

// The routing settings come back whole: a domain added or removed leaves every
// other domain, with its certificate, headers and limits, as it was.
func TestRoutingSettingsCarryEveryField(t *testing.T) {
	var settings api.AppsettingsdtoRoutingSettingsResp
	fill(reflect.ValueOf(&settings).Elem(), 0)

	req, err := RoutingSettings(&settings)
	require.NoError(t, err)

	missing, _ := unfilled(reflect.ValueOf(req).Elem(), "")
	assert.Empty(t, missing, "fields of the routing settings request the CLI does not carry back from the response")
}

// The service, resource and autoscale settings come back whole.
func TestScaleSettingsCarryEveryField(t *testing.T) {
	var service api.AppsettingsdtoServiceSettingsResp
	fill(reflect.ValueOf(&service).Elem(), 0)
	serviceReq, err := ServiceSettings(&service)
	require.NoError(t, err)
	missing, _ := unfilled(reflect.ValueOf(serviceReq).Elem(), "")
	assert.Empty(t, missing, "fields of the service settings request the CLI does not carry back")

	var resources api.AppsettingsdtoResourceSettingsResp
	fill(reflect.ValueOf(&resources).Elem(), 0)
	resourcesReq, err := ResourceSettings(&resources)
	require.NoError(t, err)
	missing, _ = unfilled(reflect.ValueOf(resourcesReq).Elem(), "")
	assert.Empty(t, missing, "fields of the resource settings request the CLI does not carry back")

	var autoscale api.AppsettingsdtoAppAutoscaleResp
	fill(reflect.ValueOf(&autoscale).Elem(), 0)
	autoscaleReq, err := Autoscale(&autoscale)
	require.NoError(t, err)
	missing, _ = unfilled(reflect.ValueOf(autoscaleReq).Elem(), "")
	assert.Empty(t, missing, "fields of the autoscale request the CLI does not carry back")
}

// A project comes back whole: its envs, its tags and its owner.
func TestProjectCarriesEveryField(t *testing.T) {
	var project api.ProjectdtoProjectResp
	fill(reflect.ValueOf(&project).Elem(), 0)

	req, err := Project(&project)
	require.NoError(t, err)

	missing, _ := unfilled(reflect.ValueOf(req).Elem(), "")
	assert.Empty(t, missing, "fields of the project request the CLI does not carry back")
	assert.Equal(t, project.Owner.Id, req.Owner.Id)
}

// The environment variables come back whole, without those HivePaaS sets.
func TestEnvVarsCarryEveryField(t *testing.T) {
	var vars api.AppsettingsdtoEnvVarsResp
	fill(reflect.ValueOf(&vars).Elem(), 0)
	for _, list := range []*[]api.BasedtoEnvVarResp{vars.RuntimeEnvVars, vars.BuildtimeEnvVars, vars.SharedEnvVars} {
		(*list)[0].IsSystem = new(false)
	}

	req, err := EnvVars(&vars)
	require.NoError(t, err)

	missing, _ := unfilled(reflect.ValueOf(req).Elem(), "")
	assert.Empty(t, missing, "fields of the environment variables request the CLI does not carry back")
}

func TestEnvVarsLeaveOutTheSystemOnes(t *testing.T) {
	vars := api.AppsettingsdtoEnvVarsResp{
		RuntimeEnvVars: &[]api.BasedtoEnvVarResp{
			{Key: "HIVEPAAS_APP_NAME", Value: "api", IsSystem: new(true), IsReadOnly: new(true)},
			{Key: "LOG_LEVEL", Value: "info"},
			{Key: "DATABASE_URL", Value: "${db.HIVEPAAS_URL}", IsLiteral: new(false), IsSystem: new(false)},
		},
		BuildtimeEnvVars: nil,
		SharedEnvVars:    &[]api.BasedtoEnvVarResp{{Key: "TZ", Value: "UTC", IsLiteral: new(true)}},
		UpdateVer:        7,
	}

	req, err := EnvVars(&vars)
	require.NoError(t, err)

	assert.Equal(t, []api.BasedtoEnvVarReq{
		{Key: "LOG_LEVEL", Value: "info"},
		{Key: "DATABASE_URL", Value: "${db.HIVEPAAS_URL}"},
	}, *req.RuntimeEnvVars)
	assert.Equal(t, []api.BasedtoEnvVarReq{}, *req.BuildtimeEnvVars, "an empty list, not null")
	assert.Equal(t, []api.BasedtoEnvVarReq{{Key: "TZ", Value: "UTC", IsLiteral: true}}, *req.SharedEnvVars)
	assert.Equal(t, 7, req.UpdateVer)
}

func TestDeploymentSettingsTakeTheRegistryAuthsID(t *testing.T) {
	settings := api.AppsettingsdtoDeploymentSettingsResp{
		ActiveMethod: api.DeploymentMethodImage,
		ImageSource: &api.AppsettingsdtoDeploymentImageSourceResp{
			Image:        "ghcr.io/acme/api:1.4.2",
			RegistryAuth: &api.SettingsBaseSettingResp{Id: "01REG", Name: "ghcr"},
		},
		UpdateVer: 3,
	}

	req, err := DeploymentSettings(&settings)
	require.NoError(t, err)

	assert.Equal(t, api.DeploymentMethodImage, req.ActiveMethod)
	assert.Equal(t, "ghcr.io/acme/api:1.4.2", req.ImageSource.Image)
	assert.Equal(t, "01REG", req.ImageSource.RegistryAuth.Id)
	assert.Nil(t, req.RepoSource)
	assert.Equal(t, 3, req.UpdateVer)
}

// maxDepth stops fill on a type that contains itself.
const maxDepth = 12

// fill gives every field of v a value that is not its zero: a pointer something
// to point at, a list and a map one item.
func fill(v reflect.Value, depth int) {
	if depth > maxDepth {
		return
	}
	switch v.Kind() { //nolint:exhaustive // the kinds the generated types use
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), depth+1)
			}
		}
	case reflect.Slice:
		list := reflect.MakeSlice(v.Type(), 1, 1)
		fill(list.Index(0), depth+1)
		v.Set(list)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fill(key, depth+1)
		elem := reflect.New(v.Type().Elem()).Elem()
		fill(elem, depth+1)
		m.SetMapIndex(key, elem)
		v.Set(m)
	case reflect.Interface:
		v.Set(reflect.ValueOf("x"))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	}
}

// unfilled are the paths of v's fields left at their zero, by their JSON names,
// and the paths of notCarried it met.
func unfilled(v reflect.Value, path string) (missing, used []string) {
	if _, skip := notCarried[path]; skip {
		return nil, []string{path}
	}
	switch v.Kind() { //nolint:exhaustive
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return []string{path}, nil
		}
		return unfilled(v.Elem(), path)
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if path != "" {
				name = path + "." + name
			}
			m, u := unfilled(v.Field(i), name)
			missing, used = append(missing, m...), append(used, u...)
		}
		return missing, used
	case reflect.Slice:
		if v.Len() == 0 {
			return []string{path}, nil
		}
		return unfilled(v.Index(0), path+"[]")
	case reflect.Map:
		if v.Len() == 0 {
			return []string{path}, nil
		}
		iter := v.MapRange()
		iter.Next()
		return unfilled(iter.Value(), path+"{}")
	default:
		if v.IsZero() {
			return []string{path}, nil
		}
		return nil, nil
	}
}
