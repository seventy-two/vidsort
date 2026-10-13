package mpv

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMPV answers each command on conn: "fail" gets an error, everything
// else succeeds and echoes the command name. Before each reply it sends an
// event, to check events and replies are told apart.
func fakeMPV(t *testing.T, conn net.Conn, got chan<- []any) {
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		var req struct {
			Command   []any `json:"command"`
			RequestID int64 `json:"request_id"`
		}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &req))
		got <- req.Command
		fmt.Fprintf(conn, `{"event":"property-change","name":"time-pos","data":1.5}`+"\n")
		status := "success"
		if req.Command[0] == "fail" {
			status = "invalid parameter"
		}
		fmt.Fprintf(conn, `{"request_id":%d,"error":%q,"data":%q}`+"\n", req.RequestID, status, req.Command[0])
	}
}

func TestCommandsAndEvents(t *testing.T) {
	ours, theirs := net.Pipe()
	got := make(chan []any, 10)
	go fakeMPV(t, theirs, got)
	c := NewClient(ours)

	events := make(chan Event, 10)
	go func() {
		for ev := range c.Events() {
			events <- ev
		}
		close(events)
	}()

	data, err := c.Command("get_property", "pause")
	require.NoError(t, err)
	assert.JSONEq(t, `"get_property"`, string(data))
	assert.Equal(t, []any{"get_property", "pause"}, <-got)

	require.NoError(t, c.SeekPercent(40))
	assert.Equal(t, []any{"seek", float64(40), "absolute-percent"}, <-got)

	require.NoError(t, c.BindMessage("DEL", "vidsort delete"))
	assert.Equal(t, []any{"keybind", "DEL", "script-message vidsort delete"}, <-got)

	_, err = c.Command("fail")
	assert.ErrorContains(t, err, "invalid parameter")

	ev := <-events
	assert.Equal(t, "property-change", ev.Event)
	assert.Equal(t, "time-pos", ev.Name)
	assert.JSONEq(t, "1.5", string(ev.Data))

	theirs.Close()
	for range events { // drains until the client closes the channel
	}
	_, err = c.Command("stop")
	assert.ErrorIs(t, err, ErrClosed)
}

func TestCommandTimesOut(t *testing.T) {
	ours, theirs := net.Pipe()
	go func() { // reads but never replies
		sc := bufio.NewScanner(theirs)
		for sc.Scan() {
		}
	}()
	c := NewClient(ours)
	c.timeout = 50 * time.Millisecond
	_, err := c.Command("stop")
	assert.ErrorContains(t, err, "no reply")
	c.mu.Lock()
	assert.Empty(t, c.pending)
	c.mu.Unlock()
}

func TestClientMessageEvent(t *testing.T) {
	ours, theirs := net.Pipe()
	c := NewClient(ours)
	go fmt.Fprintf(theirs, `{"event":"client-message","args":["vidsort","keep"]}`+"\n")
	ev := <-c.Events()
	assert.Equal(t, "client-message", ev.Event)
	assert.Equal(t, []string{"vidsort", "keep"}, ev.Args)
	theirs.Close()
}
