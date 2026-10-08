package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/client"
)

// uploadServer takes an upload as the API does, and answers with answer once
// the end came - or at once, with early.
func uploadServer(t *testing.T, answer string, early bool) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	got := &bytes.Buffer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/app/x", r.URL.Query().Get("path"))
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if early {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(answer))
			return
		}
		for {
			kind, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.TextMessage && strings.Contains(string(message), `"end"`) {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(answer))
				return
			}
			got.Write(message)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func upload(t *testing.T, srv *httptest.Server, content io.Reader) error {
	t.Helper()
	u := &Upload{URL: srv.URL, Query: map[string][]string{"path": {"/app/x"}}, Chunk: 4}
	if err := u.Open(context.Background()); err != nil {
		return err
	}
	return u.Send(context.Background(), content)
}

func TestAnUploadArrivesWhole(t *testing.T) {
	srv, got := uploadServer(t, `{"type":"done","data":{"path":"/app/x","message":"ok"}}`, false)

	err := upload(t, srv, strings.NewReader("a file of some bytes"))

	require.NoError(t, err)
	assert.Equal(t, "a file of some bytes", got.String(), "in messages of 4 bytes, in order")
}

// The server's error is the upload's, with its status: as a request's would be.
func TestAnUploadTheServerRefuses(t *testing.T) {
	srv, _ := uploadServer(t, `{"type":"error","error":{"status":409,"code":"ERR_CONFLICT",
		"detail":"a directory is there"}}`, true)

	err := upload(t, srv, strings.NewReader(strings.Repeat("x", 1<<20)))

	var apiErr *client.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.Status)
	assert.Equal(t, "a directory is there", apiErr.Info.Detail)
}

// A server without the stream answers the opening with its 404, before anything
// is read of the content.
func TestAnUploadToAServerWithoutTheStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	t.Cleanup(srv.Close)

	u := &Upload{URL: srv.URL}
	err := u.Open(context.Background())

	var apiErr *client.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Status)
	assert.Empty(t, apiErr.Info.Code, "no code: the route is not there")
}

// What cannot be read here is said to be so.
func TestAnUploadOfWhatCannotBeRead(t *testing.T) {
	srv, _ := uploadServer(t, `{"type":"done"}`, false)
	failing := io.MultiReader(strings.NewReader("abc"), iotestErrReader{})

	err := upload(t, srv, failing)

	var source *SourceError
	assert.ErrorAs(t, err, &source)
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("permission denied") }
