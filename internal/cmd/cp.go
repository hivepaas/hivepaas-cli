package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/stream"
)

// defaultCopyTimeout is how long a copy may take: a directory can be large.
const defaultCopyTimeout = 30 * time.Minute

// tarContentType is what the server answers a directory as.
const tarContentType = "application/x-tar"

type cpFlags struct {
	replica int
	timeout time.Duration
}

func (a *App) cpCmd() *cobra.Command {
	var flags cpFlags
	cmd := &cobra.Command{
		Use:   "cp SRC DST",
		Short: "Copy a file or a directory into an app's container, or out of it",
		Long: "Copy between this machine and one of an app's containers. The container's side is :PATH,\n" +
			"for the app -a names or the directory's link, or APP:PATH. A directory is copied as itself,\n" +
			"into DST; SRC/. copies what is in it instead. A file to a PATH ending in / keeps its name;\n" +
			"a file never replaces a directory of that name in the container.\n\n" +
			"The copy goes to a running container of the app: --replica picks which. A copy into it that\n" +
			"stops part way - Ctrl-C, a cut connection - leaves there what reached it: a file partly\n" +
			"written, some of a directory's files.",
		Example: "  hivepaas cp ./config.yaml :/app/config.yaml\n" +
			"  hivepaas cp ./site/. :/usr/share/nginx/html\n" +
			"  hivepaas cp api:/var/log/app.log .\n" +
			"  hivepaas cp :/data/exports ./exports --replica 2",
		Args: usageArgs(cobra.ExactArgs(2)), //nolint:mnd // SRC and DST
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.cp(cmd.Context(), args[0], args[1], flags)
		},
	}
	cmd.Flags().IntVar(&flags.replica, "replica", 0, "the replica to copy to or from, as ps numbers them")
	cmd.Flags().DurationVar(&flags.timeout, "timeout", defaultCopyTimeout, "how long the copy may take")
	return cmd
}

// containerPath is the side of a copy in an app's container: APP:PATH, or :PATH
// for the app the command line selects.
type containerPath struct {
	app, path string
}

// parseContainerPath reads arg as the container's side, or says it is a local
// path: one without a colon, or a Windows drive's (C:\...).
func parseContainerPath(arg string) (containerPath, bool) {
	app, p, found := strings.Cut(arg, ":")
	if !found || len(app) == 1 || strings.ContainsAny(app, `/\`) {
		return containerPath{}, false
	}
	return containerPath{app: app, path: p}, true
}

func (a *App) cp(ctx context.Context, src, dst string, flags cpFlags) error {
	from, fromRemote := parseContainerPath(src)
	to, toRemote := parseContainerPath(dst)
	switch {
	case fromRemote == toRemote:
		return exitcode.New(exitcode.Usage, "one of SRC and DST is in the app's container, as :PATH or APP:PATH, "+
			"and the other here")
	case fromRemote && from.path == "", toRemote && to.path == "":
		return exitcode.New(exitcode.Usage, "the container's side needs a path: :/app/config.yaml")
	}
	remote := from
	if toRemote {
		remote = to
	}
	if remote.app != "" {
		a.app = remote.app
	}
	c, err := a.clientWithTimeout(flags.timeout)
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	target, err := a.copyTarget(ctx, c, sel, flags.replica)
	if err != nil {
		return err
	}
	if toRemote {
		return a.upload(ctx, c, sel, target, src, to.path)
	}
	return a.download(ctx, c, sel, target, from.path, dst)
}

// copyTarget is the container --replica names: none, for the server's choice of
// a running one.
type copyTarget struct {
	nodeID, containerID string
}

func (a *App) copyTarget(ctx context.Context, c *client.Client, sel *selection, replica int) (copyTarget, error) {
	if replica == 0 {
		return copyTarget{}, nil
	}
	tasks, err := serviceTasks(ctx, c, sel, "running")
	if err != nil {
		return copyTarget{}, err
	}
	for _, t := range tasks {
		if t.Slot == replica && t.Node != nil && t.Status != nil && t.Status.ContainerStatus != nil &&
			t.Status.ContainerStatus.ContainerId != "" {
			return copyTarget{nodeID: t.Node.RefId, containerID: t.Status.ContainerStatus.ContainerId}, nil
		}
	}
	return copyTarget{}, exitcode.New(exitcode.NotFound, "replica %d of %s is not running: hivepaas ps%s lists them",
		replica, sel.App.Name, sel.flags())
}

// upload copies a local file, or a directory as a tar the server extracts, to
// path in the container: over the stream, which no timeout cuts, or by the form
// to a server without it.
func (a *App) upload(ctx context.Context, c *client.Client, sel *selection, target copyTarget,
	local, remotePath string,
) error {
	contentsOnly := strings.HasSuffix(filepath.ToSlash(local), "/.")
	info, err := os.Stat(local)
	if err != nil {
		return exitcode.New(exitcode.Usage, "%s: %v", local, err)
	}
	// Without overwrite=false the server lets a file replace a directory it
	// lands on, and all that directory holds.
	query := url.Values{"path": {remotePath}, "overwrite": {"false"}}
	if target.containerID != "" {
		query.Set("nodeId", target.nodeID)
		query.Set("containerId", target.containerID)
	}
	if info.IsDir() {
		query.Set("extract", "true")
		query.Set("compressionFormat", "gzip")
		query.Set("fileName", info.Name()+".tar.gz")
	} else {
		query.Set("fileName", info.Name())
		query.Set("fileSize", strconv.FormatInt(info.Size(), 10))
	}
	u := &stream.Upload{
		URL: fmt.Sprintf("%s/projects/%s/%s/apps/%s/container/file-upload/stream", c.BaseURL, sel.Project.Id,
			sel.Env, sel.App.Id),
		Query:  query,
		Header: client.Headers(c.Target),
	}
	err = u.Open(ctx)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && apiErr.Info.Code == "" {
		a.printer.Warnf("this server takes an upload in one request, and its proxy cuts an upload after 60 " +
			"seconds: a large one may fail")
		return a.uploadForm(ctx, c, sel, target, local, remotePath, info, contentsOnly)
	}
	if err != nil {
		return err
	}
	content, stop := uploadContent(local, info, contentsOnly)
	defer stop()
	total := info.Size()
	if info.IsDir() {
		total = 0
	}
	progress, done := a.progress(content, total)
	err = u.Send(ctx, progress)
	done()
	if err != nil {
		a.partlyCopied(sel.App.Name, local, remotePath, info)
	}
	var source *stream.SourceError
	switch {
	case errors.As(err, &source):
		return exitcode.New(exitcode.Failure, "reading %s: %v", local, source.Err)
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		return exitcode.Reported(exitcode.Interrupted)
	case err != nil:
		return err
	}
	a.printer.Successf("Copied %s to %s:%s.", local, sel.App.Name, remotePath)
	return nil
}

// partlyCopied says what an upload that stopped part way may have left in the
// container: what reached it stays there, written as far as it came.
func (a *App) partlyCopied(app, local, remotePath string, info fs.FileInfo) {
	if info.IsDir() {
		a.printer.Warnf("the copy stopped part way: some of %s may be in %s:%s already", local, app, remotePath)
		return
	}
	dest := remotePath
	if strings.HasSuffix(dest, "/") {
		dest = path.Join(dest, info.Name())
	}
	a.printer.Warnf("the copy stopped part way: %s:%s may be partly written", app, dest)
}

// uploadContent is what an upload sends: the file, or a directory as a gzipped
// tar - as itself, or what is in it with contentsOnly. stop ends the reading.
func uploadContent(local string, info fs.FileInfo, contentsOnly bool) (io.Reader, func()) {
	if !info.IsDir() {
		f, err := os.Open(local)
		if err != nil {
			return failingReader{err}, func() {}
		}
		return f, func() { _ = f.Close() }
	}
	pr, pw := io.Pipe()
	go func() {
		prefix := info.Name()
		if contentsOnly {
			prefix = ""
		}
		zw := gzip.NewWriter(pw)
		err := writeTar(zw, local, prefix)
		if err == nil {
			err = zw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return pr, func() { _ = pr.CloseWithError(io.ErrClosedPipe) }
}

// failingReader is a file that could not be opened: its reading fails.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// uploadForm copies by the form, in one request: what a server without the
// stream takes.
func (a *App) uploadForm(ctx context.Context, c *client.Client, sel *selection, target copyTarget,
	local, remotePath string, info fs.FileInfo, contentsOnly bool,
) error {
	body, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	fields := map[string]string{"path": remotePath, "overwrite": "false", "nodeId": target.nodeID,
		"containerId": target.containerID}
	if info.IsDir() {
		fields["extract"], fields["compressionFormat"] = "true", "tar"
	}
	written := make(chan error, 1)
	go func() {
		err := writeForm(form, fields, local, info, contentsOnly)
		written <- err
		writer.CloseWithError(err)
	}()
	resp, err := c.UploadFileToAppContainerWithBodyWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		form.FormDataContentType(), body)
	_ = body.Close()
	// What failed here is said as such, not as the server's failure.
	if localErr := <-written; localErr != nil && !errors.Is(localErr, io.ErrClosedPipe) {
		return exitcode.New(exitcode.Failure, "reading %s: %v", local, localErr)
	}
	if err = client.Check(resp, err); err != nil {
		return err
	}
	a.printer.Successf("Copied %s to %s:%s.", local, sel.App.Name, remotePath)
	return nil
}

// writeForm writes the upload's form: its fields, then the file - a directory
// as a tar of it, under its own name unless contentsOnly.
func writeForm(form *multipart.Writer, fields map[string]string, local string, info fs.FileInfo,
	contentsOnly bool,
) error {
	for name, value := range fields {
		if value == "" {
			continue
		}
		if err := form.WriteField(name, value); err != nil {
			return err //nolint:wrapcheck // the upload reports it
		}
	}
	name := info.Name()
	if info.IsDir() {
		name += ".tar"
	}
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return err //nolint:wrapcheck
	}
	if info.IsDir() {
		prefix := info.Name()
		if contentsOnly {
			prefix = ""
		}
		err = writeTar(part, local, prefix)
	} else {
		err = copyFile(part, local)
	}
	if err != nil {
		return err
	}
	return form.Close() //nolint:wrapcheck
}

func copyFile(w io.Writer, local string) error {
	f, err := os.Open(local)
	if err != nil {
		return err //nolint:wrapcheck // the upload reports it
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err //nolint:wrapcheck
}

// writeTar writes the tree under root as a tar, its entries under prefix,
// owned by root as docker cp gives them; a root that is a link is the tree it
// points to.
func writeTar(w io.Writer, root, prefix string) error {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err //nolint:wrapcheck
		}
		name := path.Join(prefix, filepath.ToSlash(rel))
		if name == "." || name == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err //nolint:wrapcheck
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(file); err != nil {
				return err //nolint:wrapcheck
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err //nolint:wrapcheck
		}
		header.Name = name
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if info.IsDir() {
			header.Name += "/"
		}
		if err = tw.WriteHeader(header); err != nil {
			return err //nolint:wrapcheck
		}
		if info.Mode().IsRegular() {
			return copyFile(tw, file)
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the upload reports it
	}
	return tw.Close() //nolint:wrapcheck
}

// download copies path out of the container: a file to local, or into it when
// it is a directory; a directory, which comes as a tar, into local.
func (a *App) download(ctx context.Context, c *client.Client, sel *selection, target copyTarget,
	remotePath, local string,
) error {
	params := &api.DownloadFileFromAppContainerParams{Path: remotePath}
	if target.containerID != "" {
		params.NodeId, params.ContainerId = &target.nodeID, &target.containerID
	}
	resp, err := c.DownloadFileFromAppContainer(ctx, sel.Project.Id, sel.Env, sel.App.Id, params)
	if err != nil {
		return client.Check(nil, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return client.CheckStatus(resp.StatusCode, body)
	}
	body, done := a.progress(resp.Body, resp.ContentLength)
	defer done()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), tarContentType) {
		// Into DST when it is there, as DST when it is not: as docker cp does.
		_, statErr := os.Stat(local)
		files, skipped, err := extractTar(body, local, os.IsNotExist(statErr))
		if err != nil {
			return exitcode.New(exitcode.Failure, "extracting into %s: %v", local, err)
		}
		if skipped > 0 {
			a.printer.Warnf("left out %d links: a link is not copied out of the container", skipped)
		}
		a.printer.Successf("Copied %s:%s into %s, %d files.", sel.App.Name, remotePath, local, files)
		return nil
	}
	file := local
	if info, err := os.Stat(local); err == nil && info.IsDir() {
		file = filepath.Join(local, path.Base(remotePath))
	}
	out, err := os.Create(file) //nolint:gosec // the file the person names
	if err != nil {
		return exitcode.New(exitcode.Failure, "%v", err)
	}
	if _, err = io.Copy(out, body); err != nil {
		_ = out.Close()
		return exitcode.New(exitcode.Failure, "writing %s: %v", file, err)
	}
	if err = out.Close(); err != nil {
		return exitcode.New(exitcode.Failure, "writing %s: %v", file, err)
	}
	a.printer.Successf("Copied %s:%s to %s.", sel.App.Name, remotePath, file)
	return nil
}

// maxErrorBody is how much of a refused download's body is read.
const maxErrorBody = 64 << 10

// extractTar writes a tar's files and directories under dir, which it makes -
// without the entries' first part with stripTop, the directory they are in;
// an entry that would land outside it is refused, and links are left out.
func extractTar(r io.Reader, dir string, stripTop bool) (files, skipped int, _ error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:mnd // a directory as mkdir makes one
		return 0, 0, err //nolint:wrapcheck // the caller says where
	}
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, skipped, nil
		}
		if err != nil {
			return files, skipped, err //nolint:wrapcheck
		}
		name := path.Clean("/" + header.Name)[1:]
		if stripTop {
			_, name, _ = strings.Cut(name, "/")
		}
		name = filepath.FromSlash(name)
		if name == "" {
			continue
		}
		if !filepath.IsLocal(name) {
			return files, skipped, fmt.Errorf("%s lands outside %s", header.Name, dir)
		}
		target := filepath.Join(dir, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0o755); err != nil { //nolint:mnd
				return files, skipped, err //nolint:wrapcheck
			}
		case tar.TypeReg:
			if err = writeEntry(tr, target, header.FileInfo().Mode().Perm()); err != nil {
				return files, skipped, err
			}
			files++
		default:
			skipped++
		}
	}
}

func writeEntry(r io.Reader, target string, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:mnd
		return err //nolint:wrapcheck // the caller says where
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm|0o200) //nolint:gosec,mnd
	if err != nil {
		return err //nolint:wrapcheck
	}
	if _, err = io.Copy(out, r); err != nil { //nolint:gosec // the size is the person's own container's
		_ = out.Close()
		return err //nolint:wrapcheck
	}
	return out.Close() //nolint:wrapcheck
}
