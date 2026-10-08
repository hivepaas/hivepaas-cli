// Package resolve finds the projects, environments and apps a person names, the
// way the MCP server does (hivepaas_app/interface/mcp/resolve.go): by id or key
// exactly, else by name ignoring case, through the list endpoints as the key's
// user - so what the user may not see is "not found", like a name that does not
// exist. None, and more than one, are both errors that list the candidates.
package resolve

import (
	"context"
	"fmt"
	"strings"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// pageLimit is one page of a list: a person's projects or an environment's apps
// fit in one, and more are read page by page.
const pageLimit = 500

// maxCandidates is how many names an error lists.
const maxCandidates = 12

// Resolver finds things by what a person calls them.
type Resolver struct {
	c *client.Client
}

func New(c *client.Client) *Resolver {
	return &Resolver{c: c}
}

// Projects are the projects the key's user can see.
func (r *Resolver) Projects(ctx context.Context) ([]api.ProjectdtoProjectResp, error) {
	var all []api.ProjectdtoProjectResp
	for offset := 0; ; offset += pageLimit {
		resp, err := r.c.ListProjectWithResponse(ctx, &api.ListProjectParams{
			PageOffset: ptr(offset), PageLimit: ptr(pageLimit),
		})
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return all, nil
		}
		page := Deref(resp.JSON200.Data)
		all = append(all, page...)
		if len(page) < pageLimit {
			return all, nil
		}
	}
}

// Project is the project input names.
func (r *Resolver) Project(ctx context.Context, input string) (*api.ProjectdtoProjectResp, error) {
	projects, err := r.Projects(ctx)
	if err != nil {
		return nil, err
	}
	project, err := pick("project", input, "", projects, func(p api.ProjectdtoProjectResp) named {
		return named{p.Id, p.Key, p.Name}
	})
	if err != nil {
		return nil, err
	}
	return &project, nil
}

// Env is the environment of project input names, by its name as the API's paths
// take it.
func Env(project *api.ProjectdtoProjectResp, input string) (string, error) {
	var envs []api.ProjectdtoProjectEnvResp
	if project.Envs != nil {
		envs = *project.Envs
	}
	env, err := pick("environment", input, " in "+project.Name, envs, func(e api.ProjectdtoProjectEnvResp) named {
		return named{e.Id, e.Name, e.Name}
	})
	if err != nil {
		return "", err
	}
	return env.Name, nil
}

// Apps are an environment's apps, with those made underneath another - a
// template's components - among them.
func (r *Resolver) Apps(ctx context.Context, projectID, env string) ([]api.AppdtoAppResp, error) {
	var all []api.AppdtoAppResp
	for offset := 0; ; offset += pageLimit {
		resp, err := r.c.ListProjectEnvAppWithResponse(ctx, projectID, env, &api.ListProjectEnvAppParams{
			PageOffset: ptr(offset), PageLimit: ptr(pageLimit), GetChildApps: ptr(true),
		})
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return all, nil
		}
		page := Deref(resp.JSON200.Data)
		all = append(all, page...)
		if len(page) < pageLimit {
			return flatten(all), nil
		}
	}
}

// flatten lists the apps nested under others beside them, once each.
func flatten(apps []api.AppdtoAppResp) []api.AppdtoAppResp {
	seen := map[string]bool{}
	var out []api.AppdtoAppResp
	var walk func([]api.AppdtoAppResp)
	walk = func(list []api.AppdtoAppResp) {
		for _, app := range list {
			if seen[app.Id] {
				continue
			}
			seen[app.Id] = true
			out = append(out, app)
			if app.ChildApps != nil {
				walk(*app.ChildApps)
			}
			if app.LogicalChildApps != nil {
				walk(*app.LogicalChildApps)
			}
		}
	}
	walk(apps)
	return out
}

// App is the app of an environment input names.
func (r *Resolver) App(ctx context.Context, projectID, projectName, env, input string) (*api.AppdtoAppResp, error) {
	apps, err := r.Apps(ctx, projectID, env)
	if err != nil {
		return nil, err
	}
	app, err := pick("app", input, fmt.Sprintf(" in %s / %s", projectName, env), apps,
		func(a api.AppdtoAppResp) named { return named{a.Id, a.Key, a.Name} })
	if err != nil {
		return nil, err
	}
	return &app, nil
}

// Volumes are the cluster volumes a project can use.
func (r *Resolver) Volumes(ctx context.Context, projectID string) ([]api.VolumedtoVolumeResp, error) {
	var all []api.VolumedtoVolumeResp
	for offset := 0; ; offset += pageLimit {
		resp, err := r.c.ListProjectClusterVolumeWithResponse(ctx, projectID, &api.ListProjectClusterVolumeParams{
			PageOffset: ptr(offset), PageLimit: ptr(pageLimit),
		})
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return all, nil
		}
		page := Deref(resp.JSON200.Data)
		all = append(all, page...)
		if len(page) < pageLimit {
			return all, nil
		}
	}
}

// Volume is the cluster volume of volumes input names.
func Volume(volumes []api.VolumedtoVolumeResp, projectName, input string) (*api.VolumedtoVolumeResp, error) {
	volume, err := pick("volume", input, " for "+projectName, volumes,
		func(v api.VolumedtoVolumeResp) named { return named{v.Id, v.Name, v.Name} })
	if err != nil {
		return nil, err
	}
	return &volume, nil
}

// named is what an input is matched against.
type named struct {
	id, key, name string
}

func (n named) label() string {
	if n.name == "" || strings.EqualFold(n.name, n.key) {
		return n.key
	}
	return n.key + " (" + n.name + ")"
}

// pick is the one item input names: by id or key exactly, else by name ignoring
// case.
func pick[T any](what, input, where string, items []T, name func(T) named) (T, error) {
	var zero T
	input = strings.TrimSpace(input)
	var exact, byName []T
	for _, item := range items {
		n := name(item)
		switch {
		case n.id == input || n.key == input:
			exact = append(exact, item)
		case strings.EqualFold(n.name, input):
			byName = append(byName, item)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	matches := append(exact, byName...) //nolint:gocritic
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		labels := make([]string, 0, len(items))
		for _, item := range items {
			labels = append(labels, name(item).label())
		}
		if len(labels) == 0 {
			return zero, exitcode.New(exitcode.NotFound, "no %s %q%s: there is none", what, input, where)
		}
		return zero, exitcode.New(exitcode.NotFound, "no %s %q%s. There: %s", what, input, where, list(labels))
	default:
		labels := make([]string, 0, len(matches))
		keyed := false
		for _, item := range matches {
			n := name(item)
			labels = append(labels, n.label()+" "+n.id)
			keyed = keyed || n.key != n.name
		}
		give := "its id"
		if keyed {
			give = "its key or id"
		}
		return zero, exitcode.New(exitcode.Usage, "%s %q%s names more than one - give %s: %s",
			what, input, where, give, list(labels))
	}
}

func list(labels []string) string {
	if len(labels) > maxCandidates {
		return strings.Join(labels[:maxCandidates], ", ") + fmt.Sprintf(" and %d more", len(labels)-maxCandidates)
	}
	return strings.Join(labels, ", ")
}

func ptr[T any](v T) *T { return &v }

// Deref is what a list the API may answer as null holds: nothing, for null.
func Deref[T any](list *[]T) []T {
	if list == nil {
		return nil
	}
	return *list
}
