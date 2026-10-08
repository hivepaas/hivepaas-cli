package resolve

import (
	"context"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
)

// RegistryAuths are the registry credentials an environment's apps can use: its
// own, and those its project and the installation share down to it - what the
// dashboard's form lists.
func (r *Resolver) RegistryAuths(ctx context.Context, projectID, env string) (
	[]api.RegistryauthdtoRegistryAuthResp, error,
) {
	var all []api.RegistryauthdtoRegistryAuthResp
	for offset := 0; ; offset += pageLimit {
		resp, err := r.c.ListProjectEnvRegistryAuthWithResponse(ctx, projectID, env,
			&api.ListProjectEnvRegistryAuthParams{PageOffset: ptr(offset), PageLimit: ptr(pageLimit)})
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

// GitCredentials are the git credentials an environment's apps can clone with,
// found as RegistryAuths are.
func (r *Resolver) GitCredentials(ctx context.Context, projectID, env string) (
	[]api.GitcredentialdtoGitCredentialResp, error,
) {
	var all []api.GitcredentialdtoGitCredentialResp
	for offset := 0; ; offset += pageLimit {
		resp, err := r.c.ListProjectEnvGitCredentialsWithResponse(ctx, projectID, env,
			&api.ListProjectEnvGitCredentialsParams{PageOffset: ptr(offset), PageLimit: ptr(pageLimit)})
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

// Setting is the setting of items input names - a credential, a job, a secret:
// by id or name exactly, else by name ignoring case. what is its kind, as the
// errors say it.
func Setting[T any](what, input, where string, items []T, idName func(T) (string, string)) (T, error) {
	return pick(what, input, where, items, func(item T) named {
		id, name := idName(item)
		return named{id, name, name}
	})
}
