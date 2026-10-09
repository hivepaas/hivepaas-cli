package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestContainerPaths(t *testing.T) {
	for arg, want := range map[string]containerPath{
		":/app/config.yaml":    {path: "/app/config.yaml"},
		"api:/var/log/app.log": {app: "api", path: "/var/log/app.log"},
		"orders-db:/backup/.":  {app: "orders-db", path: "/backup/."},
		"api:":                 {app: "api"},
	} {
		got, remote := parseContainerPath(arg)
		assert.True(t, remote, arg)
		assert.Equal(t, want, got, arg)
	}
	for _, local := range []string{"./config.yaml", "config.yaml", `C:\Users\me\x`, "C:/x", "dir/a:b"} {
		_, remote := parseContainerPath(local)
		assert.False(t, remote, local)
	}
}

// A tree written as a tar comes back the same, under its name or not.
func TestATreeThroughATar(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "css"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>hi</h1>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "css", "site.css"), []byte("h1{}"), 0o600))

	var buf bytes.Buffer
	require.NoError(t, writeTar(&buf, src, "site"))
	dst := t.TempDir()
	files, skipped, err := extractTar(&buf, dst, false)
	require.NoError(t, err)
	assert.Equal(t, 2, files)
	assert.Zero(t, skipped)
	data, err := os.ReadFile(filepath.Join(dst, "site", "css", "site.css"))
	require.NoError(t, err)
	assert.Equal(t, "h1{}", string(data))

	buf.Reset()
	require.NoError(t, writeTar(&buf, src, ""))
	assert.Equal(t, []string{"css/", "css/site.css", "index.html"}, tarNames(t, buf.Bytes()), "its contents only")
}

// An entry that would land outside the directory stops the extraction.
func TestATarCannotWriteOutside(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../../evil", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}))
	_, _ = tw.Write([]byte("x"))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}))
	require.NoError(t, tw.Close())

	dst := t.TempDir()
	files, skipped, err := extractTar(bytes.NewReader(buf.Bytes()), dst, false)
	require.NoError(t, err, "../../evil is cleaned to evil, inside")
	assert.Equal(t, 1, files)
	assert.Equal(t, 1, skipped, "a link is left out")
	_, err = os.Stat(filepath.Join(dst, "evil"))
	assert.NoError(t, err)
}

func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

// upload is what the server got in an upload's form.
type upload struct {
	fields   map[string]string
	fileName string
	file     []byte
}

func (f *fakeAPI) uploads(pattern string) *[]upload {
	got := &[]upload{}
	f.handle(pattern, func(w http.ResponseWriter, r *http.Request, _ int) {
		require.NoError(f.t, r.ParseMultipartForm(32<<20))
		u := upload{fields: map[string]string{}}
		for name, values := range r.MultipartForm.Value {
			u.fields[name] = values[0]
		}
		file, header, err := r.FormFile("file")
		require.NoError(f.t, err)
		u.fileName = header.Filename
		u.file, _ = io.ReadAll(file)
		*got = append(*got, u)
		_, _ = w.Write([]byte(`{"data":{"path":"x","message":"ok"}}`))
	})
	return got
}

// A server without the stream takes the form, as before, and the CLI says
// what that means.
func TestCpUploadsByTheFormToAServerWithoutTheStream(t *testing.T) {
	f := newFakeAPI(t)
	got := f.uploads("POST " + appPath + "/container/file-upload")
	f.json("GET "+appPath+"/service-tasks", http.StatusOK, serviceTasksBody)
	dir := t.TempDir()
	conf := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(conf, []byte("a: 1\n"), 0o600))
	site := filepath.Join(dir, "site")
	require.NoError(t, os.MkdirAll(site, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("hi"), 0o600))

	r := f.run("cp", conf, ":/app/config.yaml", "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	require.Len(t, *got, 1)
	assert.Equal(t, map[string]string{"path": "/app/config.yaml", "overwrite": "false"}, (*got)[0].fields,
		"a file landing on a directory is refused, not let replace it and all it holds")
	assert.Equal(t, "config.yaml", (*got)[0].fileName)
	assert.Equal(t, "a: 1\n", string((*got)[0].file))
	assert.Contains(t, r.stderr, "cuts an upload after 60 seconds")

	r = f.run("cp", site, "api:/usr/share/nginx/html", "-p", "shop", "-e", "production", "--replica", "2")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	require.Len(t, *got, 2)
	assert.Equal(t, map[string]string{"path": "/usr/share/nginx/html", "extract": "true", "compressionFormat": "tar",
		"overwrite": "false", "nodeId": "swarm-n2", "containerId": "c2c2c2c2c2c2c2c2"}, (*got)[1].fields,
		"the replica ps calls 2")
	assert.Equal(t, []string{"site/", "site/index.html"}, tarNames(t, (*got)[1].file), "the directory as itself")

	r = f.run("cp", site+"/.", ":/srv", "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, []string{"index.html"}, tarNames(t, (*got)[2].file), "SRC/. is what is in it")

	r = f.run("cp", conf, "./other", "-p", "shop", "-e", "production", "-a", "api")
	assert.Equal(t, exitcode.Usage, r.code, "one side is the container's")
	r = f.run("cp", conf, ":/app", "--replica", "7", "-p", "shop", "-e", "production", "-a", "api")
	assert.Equal(t, exitcode.NotFound, r.code)
}

func TestCpDownloads(t *testing.T) {
	f := newFakeAPI(t)
	var tarBody bytes.Buffer
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.csv"), []byte("1,2"), 0o600))
	require.NoError(t, writeTar(&tarBody, src, "exports"))
	f.handle("GET "+appPath+"/container/file-download", func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Query().Get("path") {
		case "/var/log/app.log":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="app.log"`)
			_, _ = w.Write([]byte("line 1\n"))
		case "/srv/my file.txt":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''my+file.txt`)
			_, _ = w.Write([]byte("x"))
		case "/data/exports":
			w.Header().Set("Content-Type", tarContentType)
			_, _ = w.Write(tarBody.Bytes())
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"ERR_NOT_FOUND","detail":"no such file"}`))
		}
	})
	dst := t.TempDir()

	r := f.run("cp", ":/var/log/app.log", dst, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	data, err := os.ReadFile(filepath.Join(dst, "app.log"))
	require.NoError(t, err)
	assert.Equal(t, "line 1\n", string(data), "a file into a directory keeps its name")

	r = f.run("cp", ":/data/exports", dst, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	data, err = os.ReadFile(filepath.Join(dst, "exports", "a.csv"))
	require.NoError(t, err)
	assert.Equal(t, "1,2", string(data))
	assert.Contains(t, r.stderr, "1 files")

	fresh := filepath.Join(dst, "copy")
	r = f.run("cp", ":/data/exports", fresh, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	_, err = os.Stat(filepath.Join(fresh, "a.csv"))
	assert.NoError(t, err, "a directory to a DST that is not there becomes it, as docker cp does")

	r = f.run("cp", ":/srv/my file.txt", dst, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	_, err = os.Stat(filepath.Join(dst, "my file.txt"))
	assert.NoError(t, err, "the path's own name, not the header's escaped one")

	r = f.run("cp", ":/nope", dst, "-p", "shop", "-e", "production", "-a", "api")
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Contains(t, r.stderr, "no such file")
}

// Into the working directory, as `cp :/data/exports .` asks.
func TestATarIntoTheWorkingDirectory(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.csv"), []byte("1"), 0o600))
	var buf bytes.Buffer
	require.NoError(t, writeTar(&buf, src, "exports"))
	t.Chdir(t.TempDir())

	files, _, err := extractTar(&buf, ".", false)

	require.NoError(t, err)
	assert.Equal(t, 1, files)
	_, err = os.Stat(filepath.Join("exports", "a.csv"))
	assert.NoError(t, err)
}

// What is uploaded is the tree a symlink points to, owned as docker cp gives it:
// by the container's root.
func TestATarOfALinkedTree(t *testing.T) {
	real := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(real, "index.html"), []byte("hi"), 0o644))
	link := filepath.Join(t.TempDir(), "site")
	require.NoError(t, os.Symlink(real, link))

	var buf bytes.Buffer
	require.NoError(t, writeTar(&buf, link, "site"))

	assert.Equal(t, []string{"site/", "site/index.html"}, tarNames(t, buf.Bytes()))
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		assert.Zero(t, h.Uid, h.Name)
		assert.Zero(t, h.Gid, h.Name)
	}
}

// A file that cannot be read here is said to be, not taken for the server's
// failure.
func TestCpOfAFileNotReadable(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST "+appPath+"/container/file-upload", func(w http.ResponseWriter, r *http.Request, _ int) {
		_, _ = io.ReadAll(r.Body) // the form ends early
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":"ERR_BAD_REQUEST","detail":"no file"}`))
	})
	file := filepath.Join(t.TempDir(), "secret.key")
	require.NoError(t, os.WriteFile(file, []byte("k"), 0o000))

	r := f.run("cp", file, ":/app/", "-p", "shop", "-e", "production", "-a", "api")

	assert.Equal(t, exitcode.Failure, r.code)
	assert.Contains(t, r.stderr, "reading "+file)
	assert.NotContains(t, r.stderr, "reaching the server")
}

// streamed is what the server got over the upload's stream.
type streamed struct {
	query   map[string]string
	content []byte
}

func (f *fakeAPI) streams(pattern string) *[]streamed {
	got := &[]streamed{}
	f.handle(pattern, func(w http.ResponseWriter, r *http.Request, _ int) {
		s := streamed{query: map[string]string{}}
		for key, values := range r.URL.Query() {
			s.query[key] = values[0]
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var content bytes.Buffer
		for {
			kind, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.TextMessage {
				break
			}
			content.Write(message)
		}
		s.content = content.Bytes()
		*got = append(*got, s)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"done","data":{"path":"x","message":"ok"}}`))
	})
	return got
}

// An upload goes over the stream, which no timeout cuts; a directory gzipped.
func TestCpUploadsOverTheStream(t *testing.T) {
	f := newFakeAPI(t)
	got := f.streams("GET " + appPath + "/container/file-upload/stream")
	f.json("GET "+appPath+"/service-tasks", http.StatusOK, serviceTasksBody)
	dir := t.TempDir()
	conf := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(conf, []byte("a: 1\n"), 0o600))
	site := filepath.Join(dir, "site")
	require.NoError(t, os.MkdirAll(site, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("hi"), 0o600))

	r := f.run("cp", conf, ":/app/config.yaml", "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	require.Len(t, *got, 1)
	assert.Equal(t, map[string]string{"path": "/app/config.yaml", "overwrite": "false", "fileName": "config.yaml",
		"fileSize": "5"}, (*got)[0].query)
	assert.Equal(t, "a: 1\n", string((*got)[0].content))
	assert.NotContains(t, r.stderr, "60 seconds")
	assert.NotContains(t, r.stderr, "part way")

	r = f.run("cp", site, "api:/srv", "-p", "shop", "-e", "production", "--replica", "2")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	require.Len(t, *got, 2)
	assert.Equal(t, map[string]string{"path": "/srv", "overwrite": "false", "extract": "true",
		"compressionFormat": "gzip", "fileName": "site.tar.gz", "nodeId": "swarm-n2",
		"containerId": "c2c2c2c2c2c2c2c2"}, (*got)[1].query)
	plain, err := gzip.NewReader(bytes.NewReader((*got)[1].content))
	require.NoError(t, err)
	tarred, err := io.ReadAll(plain)
	require.NoError(t, err)
	assert.Equal(t, []string{"site/", "site/index.html"}, tarNames(t, tarred))
}

// An upload that stops part way says what may be left in the container: what
// reached it stays there.
func TestCpUploadThatStopsPartWay(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/container/file-upload/stream", func(w http.ResponseWriter, r *http.Request, _ int) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = conn.ReadMessage()
		_ = conn.Close() // cut, with no answer
	})
	dir := t.TempDir()
	conf := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(conf, []byte("a: 1\n"), 0o600))
	site := filepath.Join(dir, "site")
	require.NoError(t, os.MkdirAll(site, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("hi"), 0o600))

	r := f.run("cp", conf, ":/app/", "-p", "shop", "-e", "production", "-a", "api")
	assert.Equal(t, exitcode.Server, r.code, r.stderr)
	assert.Contains(t, r.stderr, "api:/app/config.yaml may be partly written")

	r = f.run("cp", site, ":/srv", "-p", "shop", "-e", "production", "-a", "api")
	assert.Equal(t, exitcode.Server, r.code, r.stderr)
	assert.Contains(t, r.stderr, "some of "+site+" may be in api:/srv already")
}
