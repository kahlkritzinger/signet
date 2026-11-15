//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach lets the editor keep running, without a console window, after the command that started it exits.
func detach(c *exec.Cmd) {
	const detachedProcess, newGroup, noWindow = 0x00000008, 0x00000200, 0x08000000
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | newGroup | noWindow}
}
