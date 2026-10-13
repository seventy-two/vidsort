package mpv

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
)

// Player is an mpv process drawing into another window, plus its IPC client.
type Player struct {
	*Client
	cmd *exec.Cmd
}

// Start runs mpv embedded in the window wid and connects to it. mpv idles
// with an empty window until a file is loaded.
func Start(mpvPath string, wid uintptr) (*Player, error) {
	pipe := fmt.Sprintf(`\\.\pipe\vidsort-%d`, os.Getpid())
	cmd := exec.Command(mpvPath,
		"--wid="+strconv.FormatUint(uint64(wid), 10),
		"--input-ipc-server="+pipe,
		"--idle=yes",
		"--force-window=yes",
		"--keep-open=yes", // stay on the last frame rather than closing the file
		// Decode on the GPU where it can: 8K HEVC at 60fps is too much
		// for the CPU alone.
		"--hwdec=auto-safe",
		"--volume-max=100", // so the volume bar's 0–100 covers the range
		"--osd-level=1",
		"--msg-level=all=no",
	)
	// mpv.exe is a GUI program, but don't let it flash a console either way.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mpv: %w", err)
	}

	// mpv creates the pipe shortly after starting.
	deadline := time.Now().Add(10 * time.Second)
	for {
		timeout := time.Second
		conn, err := winio.DialPipe(pipe, &timeout)
		if err == nil {
			return &Player{Client: NewClient(conn), cmd: cmd}, nil
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			return nil, fmt.Errorf("connecting to mpv: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Quit asks mpv to exit, killing it if it hasn't within a second.
func (p *Player) Quit() {
	done := make(chan struct{})
	go func() { p.cmd.Wait(); close(done) }()
	p.Command("quit")
	p.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		p.cmd.Process.Kill()
	}
}
