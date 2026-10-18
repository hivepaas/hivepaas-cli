package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// defaultScanTimeout is how long app scan --wait waits: the first scan on a
// volume downloads Trivy's databases, a few minutes.
const defaultScanTimeout = 15 * time.Minute

// severities are the API's, the worst first.
var severities = []api.BaseVulnSeverity{api.VulnSeverityCritical, api.VulnSeverityHigh, api.VulnSeverityMedium,
	api.VulnSeverityLow, api.VulnSeverityUnknown}

// vulnsFlags are what app vulns shows, and app scan --wait after the scan.
type vulnsFlags struct {
	all      bool
	severity []string
	ignored  bool
	failOn   string
}

func (f *vulnsFlags) add(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.BoolVar(&f.all, "all", false, "list the findings no fixed version is out for too")
	fs.StringSliceVar(&f.severity, "severity", nil, "only these severities: critical,high,medium,low,unknown")
	fs.BoolVar(&f.ignored, "ignored", false, "list the findings an ignore sets aside too, with why")
	fs.StringVar(&f.failOn, "fail-on", "", "exit 11 when the app runs a fixable finding of this severity or "+
		"worse that no ignore sets aside: critical, high, medium or low")
}

// parsed checks the flags, and answers the severities to list and the worst
// one --fail-on lets pass, or "" when it is not given.
func (f *vulnsFlags) parsed() (list []api.BaseVulnSeverity, failOn api.BaseVulnSeverity, _ error) {
	for _, s := range f.severity {
		severity, err := parseSeverity(s, "--severity")
		if err != nil {
			return nil, "", err
		}
		list = append(list, severity)
	}
	if len(list) == 0 {
		list = severities
	}
	if f.failOn != "" {
		var err error
		if failOn, err = parseSeverity(f.failOn, "--fail-on"); err != nil {
			return nil, "", err
		}
		if failOn == api.VulnSeverityUnknown {
			return nil, "", exitcode.New(exitcode.Usage, "--fail-on takes critical, high, medium or low")
		}
	}
	return list, failOn, nil
}

func parseSeverity(s, flag string) (api.BaseVulnSeverity, error) {
	severity := api.BaseVulnSeverity(strings.ToUpper(strings.TrimSpace(s)))
	if !slices.Contains(severities, severity) {
		return "", exitcode.New(exitcode.Usage, "%s %q: one of critical, high, medium, low and unknown", flag, s)
	}
	return severity, nil
}

func (a *App) appVulnsCmd() *cobra.Command {
	var flags vulnsFlags
	cmd := &cobra.Command{
		Use:   "vulns [APP]",
		Short: "List the known vulnerabilities in the images an app runs",
		Long: "List what the images the app runs carry, as the server's image scanning found it when it last\n" +
			"scanned them: each image with its counts by severity, fixable/all, then the findings, the worst\n" +
			"first - those a fixed version is out for, unless --all. Findings an ignore sets aside are left\n" +
			"out of the counts and the list; --ignored lists them too, with why.\n\n" +
			"--fail-on is a gate for a CI job: the command exits 11 when the app runs a fixable finding of\n" +
			"that severity or worse that no ignore sets aside. It looks at the last scan: `hivepaas app scan\n" +
			"--wait` scans first.",
		Example: "  hivepaas app vulns api\n  hivepaas app vulns --severity critical,high --all\n" +
			"  hivepaas app vulns --fail-on high -o json",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			list, failOn, err := flags.parsed()
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			return a.showVulns(cmd.Context(), c, sel, &flags, list, failOn)
		},
	}
	flags.add(cmd)
	return cmd
}

func (a *App) appScanCmd() *cobra.Command {
	var flags vulnsFlags
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "scan [APP]",
		Short: "Scan the images an app runs for known vulnerabilities, now",
		Long: "Ask the server to scan the images the app runs now, as the Security tab's Scan now does. The\n" +
			"scan is a task; with --wait the command follows it, then lists what it found as `hivepaas app\n" +
			"vulns` does, and takes its flags. A scan that fails exits 8.",
		Example: "  hivepaas app scan api\n  hivepaas deploy && hivepaas app scan --wait --fail-on critical",
		Args:    usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			list, failOn, err := flags.parsed()
			if err != nil {
				return err
			}
			if !wait && (failOn != "" || flags.all || flags.ignored || len(flags.severity) > 0) {
				return exitcode.New(exitcode.Usage, "--fail-on, --all, --ignored and --severity need --wait")
			}
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeApp)
			if err != nil {
				return err
			}
			resp, err := c.ScanAppImagesWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
			if err = a.imageScanAnswer(ctx, c, client.Check(resp, err)); err != nil {
				return err
			}
			if resp.JSON200 == nil || resp.JSON200.Data == nil || resp.JSON200.Data.Task == nil {
				return exitcode.New(exitcode.Server, "the server answered no task for the scan")
			}
			ref := taskRef{sel: sel, id: resp.JSON200.Data.Task.Id}
			a.printer.Infof("Scanning the images of %s, task %s", sel.where(), ref.id)
			if !wait {
				a.printer.Infof("  What it finds:  hivepaas app vulns%s", sel.flags())
				if a.printer.Structured() {
					return a.printer.Data(resp.JSON200.Data)
				}
				return nil
			}
			waitCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			task, err := a.waitForTask(waitCtx, c, ref)
			if err != nil {
				return a.stoppedWaitingForTask(ctx, err, ref, timeout)
			}
			if err = a.taskOutcome(task, ref); err != nil {
				return err
			}
			return a.showVulns(ctx, c, sel, &flags, list, failOn)
		},
	}
	flags.add(cmd)
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the scan, then list what it found")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultScanTimeout, "how long to wait for the scan")
	return cmd
}

// imageScanAnswer is err, or for a server older than image scanning - whose
// router answers the path a bare 404, where a missing app's has an ErrorInfo -
// an error that says so.
func (a *App) imageScanAnswer(ctx context.Context, c *client.Client, err error) error {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Info.Code != "" {
		return err
	}
	version := "this server"
	if me, meErr := getMe(ctx, c); meErr == nil && me.Server != nil && me.Server.Version != "" {
		version = "HivePaaS " + me.Server.Version
	}
	return exitcode.New(exitcode.NotFound, "%s has no image scanning: it comes with a newer release", version)
}

func (a *App) showVulns(ctx context.Context, c *client.Client, sel *selection, flags *vulnsFlags,
	list []api.BaseVulnSeverity, failOn api.BaseVulnSeverity,
) error {
	resp, err := c.GetAppImageScanWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
	if err = a.imageScanAnswer(ctx, c, client.Check(resp, err)); err != nil {
		return err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil {
		return exitcode.New(exitcode.Server, "the server answered no scan of %s", sel.App.Name)
	}
	scan := resp.JSON200.Data
	// The gate reads every finding, whatever the flags list.
	gate := gated(resolve.Deref(scan.Findings), failOn)

	wanted := func(severity api.BaseVulnSeverity, fixed *string) bool {
		return slices.Contains(list, severity) && (flags.all || textOf(fixed) != "")
	}
	findings := slices.DeleteFunc(slices.Clone(resolve.Deref(scan.Findings)),
		func(f api.AppimagescandtoFindingResp) bool { return !wanted(f.Severity, f.FixedVersion) })
	var ignored []api.AppimagescandtoIgnoredFindingResp
	if flags.ignored {
		ignored = slices.DeleteFunc(slices.Clone(resolve.Deref(scan.Ignored)),
			func(f api.AppimagescandtoIgnoredFindingResp) bool { return !wanted(f.Severity, f.FixedVersion) })
	}
	scan.Findings, scan.Ignored = &findings, &ignored

	if a.printer.Structured() {
		if err = a.printer.Data(scan); err != nil {
			return err
		}
	} else if err = a.printVulns(sel, scan, findings, ignored); err != nil {
		return err
	}
	return a.vulnsGate(sel, scan, gate, failOn)
}

// gated are the findings --fail-on fails on: fixable, of failOn or worse.
func gated(findings []api.AppimagescandtoFindingResp, failOn api.BaseVulnSeverity) int {
	if failOn == "" {
		return 0
	}
	limit := slices.Index(severities, failOn)
	n := 0
	for _, f := range findings {
		if rank := slices.Index(severities, f.Severity); rank >= 0 && rank <= limit && textOf(f.FixedVersion) != "" {
			n++
		}
	}
	return n
}

// vulnsGate is --fail-on's answer: exit 11 when the app runs what it names,
// and an error when there is no scan to tell.
func (a *App) vulnsGate(sel *selection, scan *api.AppimagescandtoAppImageScanResp, gate int,
	failOn api.BaseVulnSeverity,
) error {
	if failOn == "" {
		return nil
	}
	if scan.ScannedAt == nil {
		return exitcode.New(exitcode.Failure, "%s's images were never scanned, so --fail-on cannot tell: "+
			"hivepaas app scan --wait%s scans them", sel.App.Name, sel.flags())
	}
	if gate == 0 {
		return nil
	}
	worse := ""
	if failOn != api.VulnSeverityCritical {
		worse = " or worse"
	}
	return exitcode.New(exitcode.Vulnerable, "%s runs %d fixable %s%s vulnerabilit%s that no ignore sets aside",
		sel.App.Name, gate, strings.ToLower(string(failOn)), worse, map[bool]string{true: "y", false: "ies"}[gate == 1])
}

func (a *App) printVulns(sel *selection, scan *api.AppimagescandtoAppImageScanResp,
	findings []api.AppimagescandtoFindingResp, ignored []api.AppimagescandtoIgnoredFindingResp,
) error {
	now := time.Now()
	if !scan.Enabled {
		a.printer.Warnf("image scanning is switched off on the server: what is listed is from the last scans.")
	}
	if scan.Task != nil {
		a.printer.Infof("A scan of %s is %s: task %s.", sel.App.Name,
			map[bool]string{true: "queued", false: "running"}[scan.Task.Status == api.TaskStatusNotStarted],
			scan.Task.Id)
	}
	if scan.ScannedAt == nil {
		a.printer.Infof("%s's images were never scanned: hivepaas app scan%s scans them.", sel.App.Name, sel.flags())
		return nil
	}
	if e := textOf(scan.Error); e != "" {
		a.printer.Warnf("the last scan scanned no image: %s", e)
	}

	images := resolve.Deref(scan.Images)
	refs := map[string]string{}
	rows := make([][]string, 0, len(images))
	for _, image := range images {
		refs[image.ImageId] = image.ImageRef
		os := strings.TrimSpace(textOf(image.OsFamily) + " " + textOf(image.OsName))
		if image.OsEosl {
			os += " (unsupported)"
		}
		rows = append(rows, append(append([]string{image.ImageRef, orDash(os)}, counts(image.Summary)...),
			output.Ago(image.ScannedAt, now)))
		if e := textOf(image.Error); e != "" {
			a.printer.Warnf("%s: the last scan failed, and what is listed is the one before: %s", image.ImageRef, e)
		}
	}
	if err := a.printer.Table([]string{"IMAGE", "OS", "CRITICAL", "HIGH", "MEDIUM", "LOW", "IGNORED", "SCANNED"},
		rows); err != nil {
		return err
	}
	a.printer.Infof("Counts are fixable/all, after ignores.")

	if len(findings) > 0 {
		fmt.Fprintln(a.stdout)
		if err := a.printer.Table(findingHeader(len(images) > 1, false), findingRows(findings, nil, refs,
			len(images) > 1)); err != nil {
			return err
		}
	}
	if len(ignored) > 0 {
		fmt.Fprintln(a.stdout)
		plain := make([]api.AppimagescandtoFindingResp, 0, len(ignored))
		for _, f := range ignored {
			plain = append(plain, api.AppimagescandtoFindingResp{VulnId: f.VulnId, Severity: f.Severity,
				PkgName: f.PkgName, InstalledVersion: f.InstalledVersion, FixedVersion: f.FixedVersion,
				ImageId: f.ImageId})
		}
		if err := a.printer.Table(findingHeader(len(images) > 1, true), findingRows(plain, ignored, refs,
			len(images) > 1)); err != nil {
			return err
		}
	}
	return nil
}

// counts are a summary's columns: fixable/all of each severity, and how many
// are ignored.
func counts(s *api.AppimagescandtoSummaryResp) []string {
	if s == nil {
		return []string{"-", "-", "-", "-", "-"}
	}
	of := func(c api.AppimagescandtoCountResp) string { return fmt.Sprintf("%d/%d", c.Fixable, c.Total) }
	return []string{of(s.Critical), of(s.High), of(s.Medium), of(s.Low), fmt.Sprint(s.Ignored)}
}

func findingHeader(withImage, ignored bool) []string {
	header := []string{"SEVERITY", "VULNERABILITY", "PACKAGE", "INSTALLED", "FIXED"}
	if withImage {
		header = append(header, "IMAGE")
	}
	if ignored {
		return append(header, "IGNORED", "REASON")
	}
	return append(header, "TITLE")
}

// maxTitle is how much of a finding's title its row shows.
const maxTitle = 60

func findingRows(findings []api.AppimagescandtoFindingResp, ignored []api.AppimagescandtoIgnoredFindingResp,
	refs map[string]string, withImage bool,
) [][]string {
	rows := make([][]string, 0, len(findings))
	for i, f := range findings {
		row := []string{string(f.Severity), f.VulnId, f.PkgName, f.InstalledVersion, orDash(textOf(f.FixedVersion))}
		if withImage {
			row = append(row, refs[f.ImageId])
		}
		if ignored != nil {
			scope, reason := "-", "-"
			if ig := ignored[i].Ignore; ig != nil {
				scope, reason = ignoreScope(ig.Scope), ig.Reason
				if by := textOf(ig.SetBy); by != "" {
					reason += " (" + by + ")"
				}
			}
			row = append(row, scope, reason)
		} else {
			row = append(row, cut(textOf(f.Title), maxTitle))
		}
		rows = append(rows, row)
	}
	return rows
}

// ignoreScope is whom an ignore applies to, as the dashboard says it.
func ignoreScope(scope api.BaseObjectScopeType) string {
	switch scope { //nolint:exhaustive // an ignore is of one of these
	case api.ObjectScopeApp:
		return "this app"
	case api.ObjectScopeProjectEnv:
		return "its env"
	case api.ObjectScopeProject:
		return "its project"
	case api.ObjectScopeGlobal:
		return "every project"
	}
	return string(scope)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
