package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

type scaleFlags struct {
	replicas, min, max, cpu, requests int
	noAutoscale                       bool
	cpus                              float32
	memory                            string
}

func (a *App) appScaleCmd() *cobra.Command {
	var flags scaleFlags
	cmd := &cobra.Command{
		Use:   "scale [APP]",
		Short: "Show or change how many replicas an app runs, and what each may use",
		Long: "Without flags, show the app's replicas, its autoscale and its limits.\n\n" +
			"--replicas runs a fixed count. --min and --max turn autoscale on, between them, on the\n" +
			"CPU (--cpu, percent of the limit) or the requests in flight (--requests); --no-autoscale\n" +
			"turns it off. --cpus and --memory limit each replica. The change applies at once, without\n" +
			"a deployment.",
		Example: "  hivepaas app scale\n" +
			"  hivepaas app scale --replicas 3\n" +
			"  hivepaas app scale --min 2 --max 10 --cpu 70\n" +
			"  hivepaas app scale --no-autoscale --replicas 2\n" +
			"  hivepaas app scale worker --cpus 0.5 --memory 512MB",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			return a.scale(cmd.Context(), cmd, flags)
		},
	}
	f := cmd.Flags()
	f.IntVar(&flags.replicas, "replicas", 0, "run this many replicas")
	f.IntVar(&flags.min, "min", 0, "autoscale: the fewest replicas")
	f.IntVar(&flags.max, "max", 0, "autoscale: the most replicas")
	f.IntVar(&flags.cpu, "cpu", 0, "autoscale on the CPU: the percent of its limit each replica is kept at")
	f.IntVar(&flags.requests, "requests", 0, "autoscale on the requests in flight each replica takes")
	f.BoolVar(&flags.noAutoscale, "no-autoscale", false, "turn autoscale off")
	f.Float32Var(&flags.cpus, "cpus", 0, "limit each replica to this many CPUs: 0.5")
	f.StringVar(&flags.memory, "memory", "", "limit each replica's memory: 512MB, 2GB")
	cmd.MarkFlagsMutuallyExclusive("no-autoscale", "min")
	cmd.MarkFlagsMutuallyExclusive("no-autoscale", "max")
	cmd.MarkFlagsMutuallyExclusive("replicas", "min")
	cmd.MarkFlagsMutuallyExclusive("replicas", "max")
	return cmd
}

func (a *App) scale(ctx context.Context, cmd *cobra.Command, flags scaleFlags) error {
	changed := func(name string) bool { return cmd.Flags().Changed(name) }
	autoscaleChange := changed("min") || changed("max") || changed("cpu") || changed("requests") ||
		changed("no-autoscale")
	replicasChange := changed("replicas")
	limitsChange := changed("cpus") || changed("memory")
	if replicasChange && flags.replicas < 0 {
		return exitcode.New(exitcode.Usage, "--replicas takes a count, not %d", flags.replicas)
	}

	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	s := &scaler{c: c, sel: sel}
	if !autoscaleChange && !replicasChange && !limitsChange {
		return a.showScale(ctx, s)
	}

	autoscale, err := s.autoscale(ctx)
	if err != nil {
		return err
	}
	if replicasChange && autoscale.Enabled && !flags.noAutoscale {
		return exitcode.New(exitcode.Invalid, "autoscale sets the replicas of %s: give --min and --max, or "+
			"--no-autoscale with --replicas", sel.App.Name)
	}
	// Autoscale first: turned off, it lets the replicas be set.
	steps := []struct {
		wanted bool
		run    func() (string, error)
	}{
		{autoscaleChange, func() (string, error) {
			return writeBack(ctx, s.autoscale, carry.Autoscale, func(req *api.AppsettingsdtoUpdateAppAutoscaleReq) (
				string, error,
			) {
				return autoscaleChanges(req, flags, changed)
			}, s.writeAutoscale)
		}},
		{replicasChange, func() (string, error) {
			return writeBack(ctx, s.service, carry.ServiceSettings, setReplicas(sel, flags.replicas), s.writeService)
		}},
		{limitsChange, func() (string, error) {
			return writeBack(ctx, s.resources, carry.ResourceSettings, setLimits(flags, changed), s.writeResources)
		}},
	}
	var done []string
	for _, step := range steps {
		if !step.wanted {
			continue
		}
		did, err := step.run()
		if err != nil {
			if len(done) > 0 {
				a.printer.Infof("%s on %s, before this:", capitalize(strings.Join(done, "; ")), sel.where())
			}
			return err
		}
		done = append(done, did)
	}
	a.printer.Successf("%s on %s.", capitalize(strings.Join(done, "; ")), sel.where())
	return nil
}

// setReplicas sets a replicated service's count.
func setReplicas(sel *selection, replicas int) func(*api.AppsettingsdtoUpdateAppServiceSettingsReq) (string, error) {
	return func(req *api.AppsettingsdtoUpdateAppServiceSettingsReq) (string, error) {
		if req.ModeSpec == nil {
			req.ModeSpec = &api.AppsettingsdtoServiceModeSpec{}
		}
		if mode := req.ModeSpec.Mode; mode != nil && *mode != api.ServiceModeReplicated {
			return "", exitcode.New(exitcode.Invalid, "%s runs as %s, not a replicated service: its replicas are "+
				"not a count", sel.App.Name, *mode)
		}
		req.ModeSpec.ServiceReplicas = &replicas
		return "replicas " + strconv.Itoa(replicas), nil
	}
}

// setLimits sets what each replica may use.
func setLimits(flags scaleFlags, changed func(string) bool) func(*api.AppsettingsdtoUpdateAppResourceSettingsReq) (
	string, error,
) {
	return func(req *api.AppsettingsdtoUpdateAppResourceSettingsReq) (string, error) {
		if req.Limits == nil {
			req.Limits = &api.AppsettingsdtoResourceLimits{}
		}
		var parts []string
		if changed("cpus") {
			req.Limits.Cpus = &flags.cpus
			parts = append(parts, "CPUs "+formatCPUs(flags.cpus))
		}
		if changed("memory") {
			req.Limits.Memory = &flags.memory
			parts = append(parts, "memory "+flags.memory)
		}
		return "limits " + strings.Join(parts, ", "), nil
	}
}

func autoscaleChanges(req *api.AppsettingsdtoUpdateAppAutoscaleReq, flags scaleFlags, changed func(string) bool) (
	string, error,
) {
	if flags.noAutoscale {
		req.Enabled = false
		return "autoscale off", nil
	}
	req.Enabled = true
	if changed("min") {
		req.MinReplicas = flags.min
	}
	if changed("max") {
		req.MaxReplicas = flags.max
	}
	if changed("cpu") {
		req.CpuTarget = flags.cpu
	}
	if changed("requests") {
		req.RequestsTarget = flags.requests
	}
	if req.MinReplicas > req.MaxReplicas {
		return "", exitcode.New(exitcode.Usage, "autoscale between %d and %d: --min is more than --max",
			req.MinReplicas, req.MaxReplicas)
	}
	return fmt.Sprintf("autoscale %d-%d replicas%s", req.MinReplicas, req.MaxReplicas, signals(req.CpuTarget,
		req.RequestsTarget)), nil
}

func signals(cpu, requests int) string {
	var on []string
	if cpu > 0 {
		on = append(on, "CPU at "+strconv.Itoa(cpu)+"%")
	}
	if requests > 0 {
		on = append(on, strconv.Itoa(requests)+" requests in flight")
	}
	if len(on) == 0 {
		return ""
	}
	return ", on " + strings.Join(on, " or ")
}

func formatCPUs(cpus float32) string { return strconv.FormatFloat(float64(cpus), 'f', -1, 32) }

func (a *App) showScale(ctx context.Context, s *scaler) error {
	service, err := s.service(ctx)
	if err != nil {
		return err
	}
	autoscale, err := s.autoscale(ctx)
	if err != nil {
		return err
	}
	resources, err := s.resources(ctx)
	if err != nil {
		return err
	}
	if a.printer.Structured() {
		return a.printer.Data(map[string]any{"service": service, "autoscale": autoscale, "resources": resources})
	}
	fmt.Fprintf(a.stdout, "%s\n", s.sel.where())
	mode := api.ServiceModeReplicated
	replicas := ""
	if service.ModeSpec != nil {
		if service.ModeSpec.Mode != nil {
			mode = *service.ModeSpec.Mode
		}
		if service.ModeSpec.ServiceReplicas != nil {
			replicas = strconv.Itoa(*service.ModeSpec.ServiceReplicas) + ", "
		}
	}
	switch {
	case autoscale.Enabled:
		fmt.Fprintf(a.stdout, "Replicas:  %d now, autoscaled between %d and %d%s\n", autoscale.Replicas,
			autoscale.MinReplicas, autoscale.MaxReplicas, signals(autoscale.CpuTarget, autoscale.RequestsTarget))
	default:
		fmt.Fprintf(a.stdout, "Replicas:  %s%s\n", replicas, mode)
	}
	limits := "none"
	if l := resources.Limits; l != nil && (l.Cpus != nil || l.Memory != nil) {
		var parts []string
		if l.Cpus != nil && *l.Cpus > 0 {
			parts = append(parts, "CPUs "+formatCPUs(*l.Cpus))
		}
		if l.Memory != nil && *l.Memory != "" {
			parts = append(parts, "memory "+*l.Memory)
		}
		limits = firstOf(strings.Join(parts, ", "), "none")
	}
	fmt.Fprintf(a.stdout, "Limits:    %s, each\n", limits)
	return nil
}

// scaler reads and writes the settings `app scale` changes.
type scaler struct {
	c   *client.Client
	sel *selection
}

func (s *scaler) service(ctx context.Context) (*api.AppsettingsdtoServiceSettingsResp, error) {
	resp, err := s.c.GetAppServiceSettingsWithResponse(ctx, s.sel.Project.Id, s.sel.Env, s.sel.App.Id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	return dataOf(resp.JSON200.Data, "service settings")
}

func (s *scaler) writeService(ctx context.Context, req *api.AppsettingsdtoUpdateAppServiceSettingsReq) error {
	resp, err := s.c.UpdateAppServiceSettingsWithResponse(ctx, s.sel.Project.Id, s.sel.Env, s.sel.App.Id, *req)
	return client.Check(resp, err)
}

func (s *scaler) autoscale(ctx context.Context) (*api.AppsettingsdtoAppAutoscaleResp, error) {
	resp, err := s.c.GetAppAutoscaleWithResponse(ctx, s.sel.Project.Id, s.sel.Env, s.sel.App.Id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	return dataOf(resp.JSON200.Data, "autoscale settings")
}

func (s *scaler) writeAutoscale(ctx context.Context, req *api.AppsettingsdtoUpdateAppAutoscaleReq) error {
	resp, err := s.c.UpdateAppAutoscaleWithResponse(ctx, s.sel.Project.Id, s.sel.Env, s.sel.App.Id, *req)
	return client.Check(resp, err)
}

func (s *scaler) resources(ctx context.Context) (*api.AppsettingsdtoResourceSettingsResp, error) {
	resp, err := s.c.GetAppResourceSettingsWithResponse(ctx, s.sel.Project.Id, s.sel.Env, s.sel.App.Id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	return dataOf(resp.JSON200.Data, "resource settings")
}

func (s *scaler) writeResources(ctx context.Context, req *api.AppsettingsdtoUpdateAppResourceSettingsReq) error {
	resp, err := s.c.UpdateAppResourceSettingsWithResponse(ctx, s.sel.Project.Id, s.sel.Env, s.sel.App.Id, *req)
	return client.Check(resp, err)
}

func dataOf[T any](data *T, what string) (*T, error) {
	if data == nil {
		return nil, exitcode.New(exitcode.Server, "the server answered no %s", what)
	}
	return data, nil
}

// writeBack reads settings, turns them into the request that writes them back,
// changes it, and writes it under the settings' updateVer: a change made
// meanwhile makes the write fail, and the settings are read, and changed, once
// more. It answers what change did.
func writeBack[Resp, Req any](ctx context.Context,
	read func(context.Context) (*Resp, error),
	toRequest func(*Resp) (*Req, error),
	change func(*Req) (string, error),
	write func(context.Context, *Req) error,
) (string, error) {
	for attempt := 0; ; attempt++ {
		settings, err := read(ctx)
		if err != nil {
			return "", err
		}
		req, err := toRequest(settings)
		if err != nil {
			return "", err
		}
		did, err := change(req)
		if err != nil {
			return "", err
		}
		err = write(ctx, req)
		var apiErr *client.APIError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.Info.Code == errUpdateVerMismatched {
			continue
		}
		return did, err
	}
}
