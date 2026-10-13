package av1

import (
	"os/exec"
	"syscall"
)

// hideWindow stops ffmpeg and ffprobe flashing up a console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
