package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

func (a *App) secretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "secret",
		Aliases: []string{"secrets"},
		Short:   "An app's secrets: values kept hidden, given to it as ${secrets.KEY} or as files",
	}
	cmd.AddCommand(a.secretLsCmd(), a.secretSetCmd(), a.secretRmCmd())
	return cmd
}

func (a *App) secretLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List an app's secrets, without their values",
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
			secrets, err := appSecrets(cmd.Context(), c, sel)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(secrets)
			}
			now := time.Now()
			rows := make([][]string, 0, len(secrets))
			for _, s := range secrets {
				rows = append(rows, storedRow(s.Key, s.Size, s.Base64, s.Inheritable, s.Inherited, s.UpdatedAt, now))
			}
			return a.printer.Table(storedColumns(colKey), rows)
		},
	}
}

type secretSetFlags struct {
	file                 string
	previews, noPreviews bool
}

func (a *App) secretSetCmd() *cobra.Command {
	var flags secretSetFlags
	cmd := &cobra.Command{
		Use:   "set KEY=VALUE... | KEY --file FILE | KEY",
		Short: "Add or change an app's secrets",
		Long: "Add or change secrets of an app. KEY alone reads the value from stdin, or asks for it at a\n" +
			"terminal without showing it: a value on the command line stays in the shell's history.\n" +
			"--file takes a file, kept as it is: a keystore, a certificate.\n\n" +
			"A secret reaches the app as ${secrets.KEY} in an env var, or as a file through a setting\n" +
			"mount; it is in the app's pull request previews too unless --no-previews.",
		Example: "  hivepaas secret set STRIPE_KEY\n" +
			"  echo -n \"$TOKEN\" | hivepaas secret set API_TOKEN\n" +
			"  hivepaas secret set KEYSTORE --file ./keystore.jks --no-previews",
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.secretSet(cmd.Context(), args, flags)
		},
	}
	cmd.Flags().StringVar(&flags.file, "file", "", "the value is this file's content")
	cmd.Flags().BoolVar(&flags.previews, "previews", false, "the app's pull request previews get it too (default)")
	cmd.Flags().BoolVar(&flags.noPreviews, "no-previews", false, "the app's pull request previews do not get it")
	cmd.MarkFlagsMutuallyExclusive("previews", "no-previews")
	return cmd
}

// secretValue is a value to write: as text, or base64 when it is not.
type secretValue struct {
	text   string
	base64 bool
}

func valueOf(data []byte) secretValue {
	if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
		return secretValue{text: string(data)}
	}
	return secretValue{text: base64.StdEncoding.EncodeToString(data), base64: true}
}

func (a *App) secretSet(ctx context.Context, args []string, flags secretSetFlags) error {
	values, err := a.secretValues(ctx, args, flags.file)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	secrets, err := appSecrets(ctx, c, sel)
	if err != nil {
		return err
	}
	previews := previewsOf(flags.previews, flags.noPreviews)
	var added, changed []string
	for _, kv := range values {
		current := ownSecret(secrets, kv.key)
		if current == nil {
			resp, err := c.CreateAppSecretWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
				api.SecretdtoCreateSecretReq{Key: kv.key, Value: kv.value.text, Base64: kv.value.base64,
					Inheritable: previews == nil || *previews})
			if err = client.Check(resp, err); err != nil {
				return a.secretsSoFar(err, added, changed, sel)
			}
			if resp.JSON201 != nil {
				a.warnMeta(resp.JSON201.Meta)
			}
			added = append(added, kv.key)
			continue
		}
		inheritable := current.Inheritable
		if previews != nil {
			inheritable = *previews
		}
		resp, err := c.UpdateAppSecretWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, current.Id,
			api.SecretdtoUpdateSecretReq{Key: current.Key, Value: kv.value.text, Base64: kv.value.base64,
				Inheritable: inheritable, Default: current.Default != nil && *current.Default,
				UpdateVer: current.UpdateVer})
		if err = client.Check(resp, err); err != nil {
			return a.secretsSoFar(err, added, changed, sel)
		}
		if resp.JSON200 != nil {
			a.warnMeta(resp.JSON200.Meta)
		}
		changed = append(changed, kv.key)
	}
	a.printer.Successf("%s on %s.", changeWords(changed, added), sel.where())
	return nil
}

// secretsSoFar says which secrets were written before err stopped the rest.
func (a *App) secretsSoFar(err error, added, changed []string, sel *selection) error {
	if len(added)+len(changed) > 0 {
		a.printer.Infof("%s on %s before this:", changeWords(changed, added), sel.where())
	}
	return err
}

type keyValue struct {
	key   string
	value secretValue
}

// secretValues are the values args give: KEY=VALUE, or KEY and the value from
// --file, stdin, or a question.
func (a *App) secretValues(ctx context.Context, args []string, file string) ([]keyValue, error) {
	if len(args) == 1 && !strings.Contains(args[0], "=") {
		key := args[0]
		var data []byte
		var err error
		switch {
		case file != "":
			data, err = os.ReadFile(file)
		case a.interactive():
			var value string
			value, err = a.promptSecret(ctx, "Value of "+key+": ", "KEY=VALUE, --file or stdin")
			data = []byte(value)
		default:
			data, err = io.ReadAll(a.stdin)
			if utf8.Valid(data) {
				// The line's end, not the value's; bytes that are not text are kept.
				data = []byte(strings.TrimSuffix(string(data), "\n"))
			}
		}
		if err != nil {
			return nil, exitcode.Wrap(exitcode.Usage, err)
		}
		return []keyValue{{key: key, value: valueOf(data)}}, nil
	}
	if file != "" {
		return nil, exitcode.New(exitcode.Usage, "--file gives one secret its value: KEY --file FILE")
	}
	pairs, err := keyValues(args)
	if err != nil {
		return nil, err
	}
	out := make([]keyValue, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, keyValue{key: pair[0], value: secretValue{text: pair[1]}})
	}
	return out, nil
}

func (a *App) secretRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm KEY...",
		Short: "Remove secrets of an app",
		Args:  usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeApp)
			if err != nil {
				return err
			}
			secrets, err := appSecrets(ctx, c, sel)
			if err != nil {
				return err
			}
			args = slices.Compact(slices.Sorted(slices.Values(args)))
			// All found before any is removed.
			found := make([]*api.SecretdtoSecretResp, 0, len(args))
			for _, key := range args {
				s := ownSecret(secrets, key)
				if s == nil {
					return exitcode.New(exitcode.NotFound, "%s is not a secret of %s", key, sel.where())
				}
				found = append(found, s)
			}
			for i, s := range found {
				resp, err := c.DeleteAppSecretWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, s.Id)
				if err = client.Check(resp, err); err != nil {
					return a.removedSoFar(err, args[:i], sel)
				}
			}
			a.printer.Successf("Removed %s from %s.", strings.Join(args, ", "), sel.where())
			return nil
		},
	}
}

// appSecrets are an app's secrets, those of its project and env among them,
// page by page: the server answers 50 unless asked.
func appSecrets(ctx context.Context, c *client.Client, sel *selection) ([]api.SecretdtoSecretResp, error) {
	var all []api.SecretdtoSecretResp
	for offset := 0; ; offset += storedPage {
		resp, err := c.ListAppSecretWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, page(offset))
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return all, nil
		}
		items := resolve.Deref(resp.JSON200.Data)
		all = append(all, items...)
		if len(items) < storedPage {
			return all, nil
		}
	}
}

// storedPage is one page of an app's secrets or config files.
const storedPage = 500

// page asks for a page of a list the spec gives no paging parameters.
func page(offset int) api.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		query := req.URL.Query()
		query.Set("pageLimit", strconv.Itoa(storedPage))
		query.Set("pageOffset", strconv.Itoa(offset))
		req.URL.RawQuery = query.Encode()
		return nil
	}
}

// warnMeta says a write's warning: saved, and not brought to the apps.
func (a *App) warnMeta(meta *api.BasedtoMeta) {
	if meta != nil && meta.Warning != nil && *meta.Warning != "" {
		a.printer.Warnf("%s", *meta.Warning)
	}
}

// removedSoFar says which were removed before err stopped the rest.
func (a *App) removedSoFar(err error, removed []string, sel *selection) error {
	if len(removed) > 0 {
		a.printer.Infof("Removed %s from %s before this:", strings.Join(removed, ", "), sel.where())
	}
	return err
}

// ownSecret is the app's own secret key names, not one it inherits.
func ownSecret(secrets []api.SecretdtoSecretResp, key string) *api.SecretdtoSecretResp {
	for i, s := range secrets {
		if s.Key == key && (s.Inherited == nil || !*s.Inherited) {
			return &secrets[i]
		}
	}
	return nil
}

// previewsOf is what --previews and --no-previews ask: nil for neither.
func previewsOf(previews, noPreviews bool) *bool {
	switch {
	case previews:
		return ptr(true)
	case noPreviews:
		return ptr(false)
	}
	return nil
}

// changeWords says what was changed and added: Changed A, added B.
func changeWords(changed, added []string) string {
	var parts []string
	if len(changed) > 0 {
		parts = append(parts, "changed "+strings.Join(changed, ", "))
	}
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	words := strings.Join(parts, ", ")
	if words == "" {
		return "Nothing changed"
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

// storedColumns head a list of what HivePaaS keeps for an app: its secrets, its
// config files.
func storedColumns(first string) []string {
	return []string{first, "SIZE", colType, "PREVIEWS", "OF", colUpdated}
}

func storedRow(name string, size *int, isBase64, inheritable bool, inherited *bool, updatedAt string,
	now time.Time,
) []string {
	return []string{name, sizeWords(size), valueKind(isBase64), yesNo(inheritable), owner(inherited),
		output.Ago(updatedAt, now)}
}

func sizeWords(size *int) string {
	if size == nil {
		return "-"
	}
	const kb = 1024
	if *size < kb {
		return strconv.Itoa(*size) + " B"
	}
	return fmt.Sprintf("%.1f KB", float64(*size)/kb)
}

func valueKind(isBase64 bool) string {
	if isBase64 {
		return "binary"
	}
	return "text"
}

// owner says whose a setting an app sees is: its own, or its project's or env's.
func owner(inherited *bool) string {
	if inherited != nil && *inherited {
		return "inherited"
	}
	return "this app"
}
