//go:build !windows

package av1

import "os/exec"

func hideWindow(*exec.Cmd) {}
