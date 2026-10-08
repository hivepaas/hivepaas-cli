package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"

	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// defaultChunk is the size of each message an upload sends: well under the
// server's 4 MiB.
const defaultChunk = 256 << 10

// SourceError is an upload's failure to read what it sends: a local file's, not
// the server's.
type SourceError struct{ Err error }

func (e *SourceError) Error() string { return e.Err.Error() }
func (e *SourceError) Unwrap() error { return e.Err }

// Upload sends content over the API's websocket: binary messages, then
// {"type":"end"} as a text message. The server answers one text message,
// {"type":"done","data"} or {"type":"error","error"}, and closes. No timeout
// of the server's or its proxy's cuts it, as they cut a request.
type Upload struct {
	// URL is the upload's address: https://paas.example.com/api/.../stream.
	URL    string
	Query  url.Values
	Header http.Header
	Dialer *websocket.Dialer
	// Chunk is the size of each message: 256 KiB when zero.
	Chunk int

	conn *websocket.Conn
}

// Open opens the stream. A refusal is the server's APIError, before anything
// is read of what is to be sent.
func (u *Upload) Open(ctx context.Context) error {
	conn, err := dial(ctx, u.Dialer, u.URL, u.Query, u.Header)
	if err != nil {
		return err
	}
	u.conn = conn
	return nil
}

// answer is the server's last message.
type answer struct {
	Type  string          `json:"type"`
	Error json.RawMessage `json:"error"`
}

// Send sends content, then the end, and answers what the server answers: nil
// for done, its APIError for an error. The answer can come before all is sent -
// a copy that failed - and is the answer then.
func (u *Upload) Send(ctx context.Context, content io.Reader) error {
	defer u.conn.Close()
	answered := make(chan error, 1)
	go func() { answered <- readAnswer(u.conn) }()

	sent := make(chan error, 1)
	go func() { sent <- u.send(content) }()

	select {
	case <-ctx.Done():
		_ = u.conn.Close()
		return ctx.Err() //nolint:wrapcheck // the caller tells an interrupt
	case err := <-answered:
		// The server answered first: its answer is the outcome.
		return err
	case err := <-sent:
		var source *SourceError
		if errors.As(err, &source) {
			return err
		}
		select {
		case <-ctx.Done():
			_ = u.conn.Close()
			return ctx.Err() //nolint:wrapcheck
		case answer := <-answered:
			return answer
		}
	}
}

func (u *Upload) send(content io.Reader) error {
	size := u.Chunk
	if size <= 0 {
		size = defaultChunk
	}
	buf := make([]byte, size)
	for {
		n, err := content.Read(buf)
		if n > 0 {
			if writeErr := u.conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
				return fmt.Errorf("sending: %w", writeErr)
			}
		}
		if errors.Is(err, io.EOF) {
			end, _ := json.Marshal(map[string]string{"type": "end"})
			return u.conn.WriteMessage(websocket.TextMessage, end) //nolint:wrapcheck // the answer says more
		}
		if err != nil {
			return &SourceError{Err: err}
		}
	}
}

// readAnswer reads the server's last message, and what it says.
func readAnswer(conn *websocket.Conn) error {
	for {
		kind, message, err := conn.ReadMessage()
		if err != nil {
			return exitcode.Wrap(exitcode.Server, fmt.Errorf("the server ended the upload without an answer: %w", err))
		}
		if kind != websocket.TextMessage {
			continue
		}
		var a answer
		if err = json.Unmarshal(message, &a); err != nil {
			continue
		}
		switch a.Type {
		case "done":
			return nil
		case "error":
			var info struct {
				Status int `json:"status"`
			}
			_ = json.Unmarshal(a.Error, &info)
			status := info.Status
			if status < http.StatusBadRequest {
				status = http.StatusInternalServerError
			}
			return client.CheckStatus(status, a.Error)
		}
	}
}
