// Package mpv controls an mpv process through its JSON IPC protocol
// (https://mpv.io/manual/master/#json-ipc).
package mpv

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Event is an asynchronous message from mpv, such as "end-file",
// "property-change" or "client-message".
type Event struct {
	Event  string          `json:"event"`
	Name   string          `json:"name,omitempty"`   // property-change: the property
	Data   json.RawMessage `json:"data,omitempty"`   // property-change: its new value
	Args   []string        `json:"args,omitempty"`   // client-message: the script-message arguments
	Reason string          `json:"reason,omitempty"` // end-file: eof, stop, quit, error, redirect
}

// response is mpv's reply to a command.
type response struct {
	RequestID int64           `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

// ErrClosed is returned by commands sent after the connection has gone.
var ErrClosed = errors.New("mpv: connection closed")

// Client is one IPC connection to mpv. It is safe for concurrent use.
type Client struct {
	conn    io.ReadWriteCloser
	timeout time.Duration
	events  chan Event

	writeMu sync.Mutex // keeps command lines from interleaving

	mu      sync.Mutex // guards nextID, pending and closed
	nextID  int64
	pending map[int64]chan response
	closed  bool
}

// NewClient starts reading from conn. Events arrive on Events() until the
// connection closes, when that channel is closed.
func NewClient(conn io.ReadWriteCloser) *Client {
	c := &Client{
		conn:    conn,
		timeout: 5 * time.Second,
		events:  make(chan Event, 64),
		pending: map[int64]chan response{},
	}
	go c.readLoop()
	return c
}

// Events returns the channel of asynchronous events. It must be drained:
// replies to commands queue behind unread events.
func (c *Client) Events() <-chan Event { return c.events }

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var probe struct {
			Event     string `json:"event"`
			RequestID *int64 `json:"request_id"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		if probe.Event != "" {
			var ev Event
			if json.Unmarshal(line, &ev) == nil {
				c.events <- ev
			}
			continue
		}
		if probe.RequestID == nil {
			continue
		}
		var r response
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[r.RequestID]
		delete(c.pending, r.RequestID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}

	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.events)
}

// Command runs an mpv input command, e.g. Command("loadfile", path), and
// returns its result data.
func (c *Client) Command(args ...any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	msg, err := json.Marshal(map[string]any{"command": args, "request_id": id})
	if err != nil {
		return nil, err
	}
	c.writeMu.Lock()
	_, err = c.conn.Write(append(msg, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return nil, err
	}

	select {
	case r, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if r.Error != "success" {
			return nil, fmt.Errorf("mpv %v: %s", args[0], r.Error)
		}
		return r.Data, nil
	case <-time.After(c.timeout):
		c.forget(id)
		return nil, fmt.Errorf("mpv %v: no reply after %s", args[0], c.timeout)
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// LoadFile replaces whatever is playing with path.
func (c *Client) LoadFile(path string) error {
	_, err := c.Command("loadfile", path, "replace")
	return err
}

// Stop stops playback and closes the current file.
func (c *Client) Stop() error {
	_, err := c.Command("stop")
	return err
}

// SeekPercent jumps to pct (0-100) of the way through the file.
func (c *Client) SeekPercent(pct int) error {
	_, err := c.Command("seek", pct, "absolute-percent")
	return err
}

// Seek moves by secs seconds (negative goes back).
func (c *Client) Seek(secs float64) error {
	_, err := c.Command("seek", secs, "relative")
	return err
}

// TogglePause pauses or resumes playback.
func (c *Client) TogglePause() error {
	_, err := c.Command("cycle", "pause")
	return err
}

// MultiplySpeed scales the playback speed by f.
func (c *Client) MultiplySpeed(f float64) error {
	_, err := c.Command("multiply", "speed", f)
	return err
}

// Observe asks mpv to send a property-change event whenever name changes.
func (c *Client) Observe(id int, name string) error {
	_, err := c.Command("observe_property", id, name)
	return err
}

// BindMessage makes key (in mpv's input.conf syntax) send a client-message
// event carrying msg, so shortcuts work while mpv's own window has focus.
func (c *Client) BindMessage(key, msg string) error {
	_, err := c.Command("keybind", key, "script-message "+msg)
	return err
}
