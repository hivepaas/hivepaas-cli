package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellServer is the API's terminal: it says which container, echoes what is
// typed, records the resizes, and ends the shell on Ctrl-D.
type shellServer struct {
	mu      sync.Mutex
	query   string
	resizes []string
}

func (s *shellServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.query = r.URL.RawQuery
	s.mu.Unlock()
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	init, _ := json.Marshal(map[string]string{"type": "init", "containerId": "c0ffee", "nodeId": "n1"})
	_ = conn.WriteMessage(websocket.TextMessage, init)
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			s.mu.Lock()
			s.resizes = append(s.resizes, string(data))
			s.mu.Unlock()
			continue
		}
		if bytes.IndexByte(data, ctrlD) >= 0 {
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte("exit\r\n"))
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte("$ "), data...))
	}
}

func TestTerminalRunsAShell(t *testing.T) {
	srv := &shellServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resize := make(chan Size, 1)
	resize <- Size{Width: 120, Height: 40}
	var out bytes.Buffer
	var opened string
	term := &Terminal{URL: ts.URL + "/api/terminal", Query: map[string][]string{"shell": {"bash"}, "w": {"80"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The input waits for the resize to be sent, as a person types after the
	// window has its size.
	in := &slowReader{r: strings.NewReader("ls -la\n"), wait: 100 * time.Millisecond}
	err := term.Run(ctx, Session{In: in, Out: &out, Resize: resize,
		Opened: func(container, node string) { opened = container + "@" + node }})

	require.NoError(t, err, "the shell ended when its input did")
	assert.Equal(t, "c0ffee@n1", opened)
	assert.Equal(t, "$ ls -la\nexit\r\n", out.String(), "the output as it came, the init message not in it")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Equal(t, "shell=bash&w=80", srv.query)
	assert.Equal(t, []string{`{"height":40,"type":"resize","width":120}`}, srv.resizes)
}

func TestTerminalRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":403,"code":"ERR_FEATURE_DISABLED","detail":"app terminal is disabled"}`))
	}))
	defer ts.Close()

	err := (&Terminal{URL: ts.URL}).Run(context.Background(), Session{In: strings.NewReader(""), Out: &bytes.Buffer{}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "app terminal is disabled")
}

type slowReader struct {
	r    *strings.Reader
	wait time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.wait)
	return s.r.Read(p)
}
