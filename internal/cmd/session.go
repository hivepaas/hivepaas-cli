package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/config"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func (a *App) loginCmd() *cobra.Command {
	var keyID, name string
	var withSecret, insecure bool
	cmd := &cobra.Command{
		Use:   "login [URL]",
		Short: "Log in to an installation with an API key",
		Long: "Log in to an installation with an API key, made in its dashboard under Settings, API keys, " +
			"with the actions the commands need: read, execute for deploy and restart, write for changes.\n\n" +
			"The secret goes to the OS keychain. In CI, set HIVEPAAS_URL and HIVEPAAS_API_KEY instead.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			var rawURL, secret string
			var err error
			if len(args) == 1 {
				rawURL = args[0]
			} else if rawURL, err = a.prompt("URL: ", "the URL"); err != nil {
				return err
			}
			if keyID == "" {
				if keyID, err = a.prompt("API key ID: ", "--key-id"); err != nil {
					return err
				}
			}
			if withSecret {
				data, readErr := io.ReadAll(a.stdin)
				if readErr != nil {
					return fmt.Errorf("reading the secret: %w", readErr)
				}
				secret = strings.TrimSpace(string(data))
			} else if secret, err = a.promptSecret("API key secret: ", "--with-secret and the secret on stdin"); err != nil {
				return err
			}
			return a.login(cmd.Context(), config.NormalizeURL(rawURL), keyID, secret, name, insecure)
		},
	}
	cmd.Flags().StringVar(&keyID, "key-id", "", "the API key's id")
	cmd.Flags().BoolVar(&withSecret, "with-secret", false, "read the key's secret from stdin")
	cmd.Flags().StringVar(&name, "name", "", "the context's name (default: the URL's host)")
	cmd.Flags().BoolVar(&insecure, "insecure-storage", false,
		"keep the secret in a file readable only by you when the OS keychain cannot be used")
	return cmd
}

func (a *App) login(ctx context.Context, baseURL, keyID, secret, name string, insecure bool) error {
	target := &config.Target{URL: baseURL, KeyID: keyID, Secret: secret}
	c, err := a.clientOf(target)
	if err != nil {
		return err
	}
	me, err := getMe(ctx, c)
	if err != nil {
		return err
	}
	if name == "" {
		name = hostOf(baseURL)
	}
	if err = a.secrets.Set(baseURL, keyID, secret, insecure); err != nil {
		return exitcode.Wrap(exitcode.Failure, err)
	}
	user := ""
	if me.User != nil {
		user = me.User.Email
	}
	a.cfg.Contexts[name] = &config.Context{URL: baseURL, KeyID: keyID, User: user}
	a.cfg.Current = name
	if err = a.cfg.Save(); err != nil {
		return err
	}
	a.printer.Successf("Logged in to %s as %s%s.", baseURL, user, serverVersion(me.Server))
	a.printer.Infof("Saved as context %q, now the current one.", name)
	if me.Server != nil && me.Server.MinCliApiLevel > api.APILevel {
		a.printer.Warnf("this server takes changes only from a CLI built for API level %d; this one is at %d. "+
			"Update it to make changes.", me.Server.MinCliApiLevel, api.APILevel)
	}
	return nil
}

func hostOf(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}

func serverVersion(server *api.SessiondtoServerInfoResp) string {
	if server == nil {
		return ""
	}
	return fmt.Sprintf(" (HivePaaS %s)", server.Version)
}

// getMe is the session the key opens: who it is, and what the server runs.
func getMe(ctx context.Context, c *client.Client) (*api.SessiondtoGetMeDataResp, error) {
	resp, err := c.GetMeWithResponse(ctx, &api.GetMeParams{})
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil {
		return nil, exitcode.New(exitcode.Server, "the server answered no session")
	}
	return resp.JSON200.Data, nil
}

func (a *App) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the current context, or the one --context names, and its key's secret",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			name := a.contextName
			if name == "" {
				name = a.cfg.Current
			}
			return a.removeContext(name)
		},
	}
}

func (a *App) removeContext(name string) error {
	ctx := a.cfg.Contexts[name]
	if name == "" || ctx == nil {
		return exitcode.New(exitcode.NotFound, "there is no context %q to log out of", name)
	}
	if err := a.secrets.Delete(ctx.URL, ctx.KeyID); err != nil {
		return err
	}
	delete(a.cfg.Contexts, name)
	if a.cfg.Current == name {
		a.cfg.Current = ""
	}
	if err := a.cfg.Save(); err != nil {
		return err
	}
	a.printer.Successf("Logged out of %s (context %q).", ctx.URL, name)
	return nil
}

func (a *App) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Who the key acts as, on which installation",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			me, err := getMe(cmd.Context(), c)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(me)
			}
			user := ""
			if me.User != nil {
				user = me.User.Email
			}
			where := c.Target.URL
			if c.Target.Context != "" {
				where += " (context " + c.Target.Context + ")"
			}
			fmt.Fprintf(a.stdout, "%s on %s\n", user, where)
			if me.Server != nil {
				fmt.Fprintf(a.stdout, "HivePaaS %s, API level %d; this CLI: API level %d\n",
					me.Server.Version, me.Server.ApiLevel, api.APILevel)
			}
			return nil
		},
	}
}
