package cmd

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// protocolHTTP is a domain served over HTTP, which a new one is.
const protocolHTTP = "http"

func (a *App) domainCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "domain", Aliases: []string{"domains"}, Short: "An app's domains"}
	cmd.AddCommand(a.domainLsCmd(), a.domainAddCmd(), a.domainRmCmd())
	return cmd
}

func (a *App) domainLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List an app's domains",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			settings, err := routingSettings(cmd.Context(), c, sel)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(settings)
			}
			domains := resolve.Deref(settings.Domains)
			rows := make([][]string, 0, len(domains))
			for _, d := range domains {
				rows = append(rows, []string{d.Domain, string(d.Protocol), strconv.Itoa(d.ContainerPort),
					yesNo(d.ForceHttps != nil && *d.ForceHttps), yesNo(d.Enabled)})
			}
			if err = a.printer.Table([]string{"DOMAIN", "PROTOCOL", "PORT", "FORCE HTTPS", "ENABLED"}, rows); err != nil {
				return err
			}
			switch {
			case !settings.ExposePublicly && len(domains) > 0:
				a.printer.Warnf("%s is not exposed publicly: its domains are not served", sel.App.Name)
			case len(domains) == 0 && settings.DomainSuggestion != "":
				// The suggestion is a pattern, <name>.example.com: the app's key
				// fills it, so the line can be run as it is.
				suggestion := strings.ReplaceAll(settings.DomainSuggestion, "<name>",
					strings.ReplaceAll(sel.App.Key, "_", "-"))
				a.printer.Infof("No domain yet: hivepaas domain add %s%s", suggestion, sel.flags())
			}
			return nil
		},
	}
}

func (a *App) domainAddCmd() *cobra.Command {
	var port int
	var noForceHTTPS bool
	cmd := &cobra.Command{
		Use:   "add DOMAIN",
		Short: "Serve an app on a domain",
		Long: "Serve an app on a domain, over HTTP, its requests to HTTP sent on to HTTPS unless\n" +
			"--no-force-https. The port is the app's, the first domain's, or --port. An app not\n" +
			"exposed publicly is exposed: a domain is served only then. Headers, limits, paths and\n" +
			"certificates are the dashboard's.",
		Example: "  hivepaas domain add api.shop.example.com\n" +
			"  hivepaas domain add admin.shop.example.com --port 9000",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			exposed := false
			return a.changeRouting(cmd.Context(), func(req *api.AppsettingsdtoUpdateAppRoutingSettingsReq) (
				string, error,
			) {
				domains := resolve.Deref(req.Domains)
				if slices.ContainsFunc(domains, func(d api.AppsettingsdtoDomainReq) bool { return d.Domain == domain }) {
					return "", exitcode.New(exitcode.Invalid, "%s is a domain of the app already", domain)
				}
				containerPort := port
				switch {
				case containerPort > 0:
				case len(domains) > 0 && domains[0].ContainerPort > 0:
					containerPort = domains[0].ContainerPort
				default:
					containerPort = req.Port
				}
				if containerPort <= 0 {
					return "", exitcode.New(exitcode.Usage, "the app has no port to serve %s on: give --port", domain)
				}
				domains = append(domains, api.AppsettingsdtoDomainReq{
					Domain: domain, Enabled: true, Protocol: protocolHTTP, ContainerPort: containerPort,
					ForceHttps: !noForceHTTPS,
				})
				req.Domains = &domains
				exposed = !req.ExposePublicly
				req.ExposePublicly = true
				did := "added " + domain + ", port " + strconv.Itoa(containerPort)
				if exposed {
					did += ", and exposed the app publicly"
				}
				return did, nil
			})
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "the app's port the domain is served from")
	cmd.Flags().BoolVar(&noForceHTTPS, "no-force-https", false, "serve plain HTTP too, without sending it on to HTTPS")
	return cmd
}

func (a *App) domainRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm DOMAIN",
		Short: "Stop serving an app on a domain",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			return a.changeRouting(cmd.Context(), func(req *api.AppsettingsdtoUpdateAppRoutingSettingsReq) (
				string, error,
			) {
				domains := resolve.Deref(req.Domains)
				i := slices.IndexFunc(domains, func(d api.AppsettingsdtoDomainReq) bool { return d.Domain == domain })
				if i < 0 {
					names := make([]string, 0, len(domains))
					for _, d := range domains {
						names = append(names, d.Domain)
					}
					return "", exitcode.New(exitcode.NotFound, "%s is not a domain of the app: %s", domain,
						firstOf(strings.Join(names, ", "), "it has none"))
				}
				domains = slices.Delete(domains, i, i+1)
				req.Domains = &domains
				return "removed " + domain, nil
			})
		},
	}
}

// changeRouting reads an app's routing settings, changes them, and writes them
// back under their updateVer, every domain carried over whole; a change made
// meanwhile makes the write fail, and they are read, and changed, once more.
func (a *App) changeRouting(ctx context.Context,
	change func(req *api.AppsettingsdtoUpdateAppRoutingSettingsReq) (string, error),
) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		settings, err := routingSettings(ctx, c, sel)
		if err != nil {
			return err
		}
		req, err := carry.RoutingSettings(settings)
		if err != nil {
			return err
		}
		did, err := change(req)
		if err != nil {
			return err
		}
		resp, err := c.UpdateAppRoutingSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, *req)
		err = client.Check(resp, err)
		var apiErr *client.APIError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.Info.Code == errUpdateVerMismatched {
			continue
		}
		if err != nil {
			return err
		}
		a.printer.Successf("%s on %s.", capitalize(did), sel.where())
		return nil
	}
}

func routingSettings(ctx context.Context, c *client.Client, sel *selection) (
	*api.AppsettingsdtoRoutingSettingsResp, error,
) {
	resp, err := c.GetAppRoutingSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil {
		return nil, exitcode.New(exitcode.Server, "the server answered no routing settings")
	}
	return resp.JSON200.Data, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
