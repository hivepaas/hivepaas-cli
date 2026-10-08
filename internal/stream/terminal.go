package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

// ctrlD ends a shell whose input has ended: the terminal's end of file.
const ctrlD = 0x04

// Size is a terminal's size in characters.
type Size struct {
	Width, Height uint
}

// Terminal is a shell in an app's container, over the API's websocket: the
// container's output comes as binary messages, the keys typed go as binary
// messages, and a resize as a text message, {"type":"resize","width","height"}.
// The server opens it with a text message naming the container,
// {"type":"init","containerId","nodeId"}, and closes it when the shell ends.
type Terminal struct {
	// URL is the terminal's address: https://paas.example.com/api/.../terminal.
	URL    string
	Query  url.Values
	Header http.Header
	Dialer *websocket.Dialer
}

// Session is what a terminal is run with.
type Session struct {
	In  io.Reader
	Out io.Writer
	// Resize, when not nil, gives the terminal's new size whenever it changes.
	Resize <-chan Size
	// Opened is told which container the shell runs in.
	Opened func(containerID, nodeID string)
}

// Run runs the terminal until the shell ends, the input ends and the shell with
// it, or ctx is done.
func (t *Terminal) Run(ctx context.Context, s Session) error {
	conn, err := dial(ctx, t.Dialer, t.URL, t.Query, t.Header)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	// gorilla/websocket takes one writer at a time: the keys and the resizes
	// share the connection.
	var writing sync.Mutex
	write := func(messageType int, data []byte) error {
		writing.Lock()
		defer writing.Unlock()
		return conn.WriteMessage(messageType, data)
	}

	go t.send(s.In, write)
	if s.Resize != nil {
		go func() {
			for size := range s.Resize {
				msg, _ := json.Marshal(map[string]any{"type": "resize", "width": size.Width, "height": size.Height})
				if write(websocket.TextMessage, msg) != nil {
					return
				}
			}
		}()
	}

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() //nolint:wrapcheck // the caller's own
			}
			return nil // the server closes the terminal when the shell ends
		}
		if messageType == websocket.TextMessage {
			var init struct {
				Type        string `json:"type"`
				ContainerID string `json:"containerId"`
				NodeID      string `json:"nodeId"`
			}
			if json.Unmarshal(data, &init) == nil && init.Type == "init" {
				if s.Opened != nil {
					s.Opened(init.ContainerID, init.NodeID)
				}
				continue
			}
		}
		if _, err = s.Out.Write(data); err != nil {
			return err //nolint:wrapcheck
		}
	}
}

// send sends what is typed. When the input ends - a script's, piped in - the
// shell is sent the end of file a terminal sends, and ends with it.
func (t *Terminal) send(in io.Reader, write func(int, []byte) error) {
	buf := make([]byte, readSize)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if write(websocket.BinaryMessage, buf[:n]) != nil {
				return
			}
		}
		if errors.Is(err, io.EOF) {
			_ = write(websocket.BinaryMessage, []byte{ctrlD})
			return
		}
		if err != nil {
			return
		}
	}
}

// readSize is how much of the input one message carries at most.
const readSize = 4096
