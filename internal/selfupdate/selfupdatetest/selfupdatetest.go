// Package selfupdatetest publishes releases of the CLI for tests: a list signed
// with test keys, as hivepaas's releasesign signs one under -context cli, and
// archives as goreleaser makes them.
package selfupdatetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
)

// binaryMode is the mode goreleaser gives the binary in an archive.
const binaryMode = 0o755

// Signer holds a pair of test release keys, one of each algorithm.
type Signer struct {
	ed ed25519.PrivateKey
	ml *mldsa.PrivateKey
}

func NewSigner(t testing.TB) *Signer {
	t.Helper()
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ml, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	return &Signer{ed: ed, ml: ml}
}

// Keys are the signer's public keys as the CLI loads them, test-ed and test-ml.
func (s *Signer) Keys(t testing.TB) selfupdate.Keys {
	t.Helper()
	fsys := fstest.MapFS{}
	for id, pub := range map[string]any{"test-ed": s.ed.Public(), "test-ml": s.ml.PublicKey()} {
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		fsys[id+".pub.pem"] = &fstest.MapFile{Data: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})}
	}
	keys, err := selfupdate.LoadKeys(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// Sign is data in an envelope, signed with both keys under context.
func (s *Signer) Sign(t testing.TB, data []byte, context string) []byte {
	t.Helper()
	edSig, err := s.ed.Sign(nil, data, &ed25519.Options{Context: context})
	if err != nil {
		t.Fatal(err)
	}
	mlSig, err := s.ml.SignDeterministic(data, &mldsa.Options{Context: context})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	env, err := json.Marshal(map[string]any{
		"payload": base64.StdEncoding.EncodeToString(data),
		"sha256":  hex.EncodeToString(sum[:]),
		"signatures": []map[string]string{
			{"keyId": "test-ed", "alg": "ed25519", "sig": base64.StdEncoding.EncodeToString(edSig)},
			{"keyId": "test-ml", "alg": "ml-dsa-65", "sig": base64.StdEncoding.EncodeToString(mlSig)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// TarGz is an archive with one file at its top, as goreleaser packs a binary.
func TarGz(t testing.TB, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: binaryMode, Size: int64(len(content)),
		Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Zip is a zip archive with one file, as goreleaser packs a Windows binary.
func Zip(t testing.TB, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Publication is a server of releases: the signed list at /list, and each
// release's archive for the platform the test runs on at /download/<v>/<file>.
type Publication struct {
	*httptest.Server

	mu       sync.Mutex
	files    map[string][]byte
	requests map[string]int
}

// Publish serves releases: binaries by version, channels as the list names them.
func Publish(t testing.TB, signer *Signer, channels map[string]string, binaries map[string][]byte) *Publication {
	t.Helper()
	p := &Publication{files: map[string][]byte{}, requests: map[string]int{}}
	m := selfupdate.Manifest{Channels: channels, Releases: map[string]selfupdate.Release{}}
	platform := selfupdate.Platform(runtime.GOOS, runtime.GOARCH)
	for name, binary := range binaries {
		v, err := selfupdate.ParseVersion(name)
		if err != nil {
			t.Fatal(err)
		}
		file := selfupdate.ArchiveFile(v, platform)
		archive := TarGz(t, "hivepaas", binary)
		if runtime.GOOS == "windows" {
			archive = Zip(t, "hivepaas.exe", binary)
		}
		sum := sha256.Sum256(archive)
		m.Releases[v.String()] = selfupdate.Release{ReleaseDate: "2026-10-20", Archives: map[string]selfupdate.Archive{
			platform: {File: file, SHA256: hex.EncodeToString(sum[:])},
		}}
		p.files["/download/"+v.String()+"/"+file] = archive
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p.files["/list"] = signer.Sign(t, data, selfupdate.Context)
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests[r.URL.Path]++
		content, found := p.files[r.URL.Path]
		p.mu.Unlock()
		if !found {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	}))
	t.Cleanup(p.Close)
	return p
}

// Replace serves content at path instead of what was published there.
func (p *Publication) Replace(path string, content []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files[path] = content
}

// Requests is how many times path was asked for.
func (p *Publication) Requests(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[path]
}

// Updater reads the publication, trusting keys.
func (p *Publication) Updater(keys selfupdate.Keys) *selfupdate.Updater {
	return &selfupdate.Updater{
		HTTP: p.Client(), Keys: keys, ListURL: p.URL + "/list", DownloadURL: p.URL + "/download",
	}
}
