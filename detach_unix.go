//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach lets the editor keep running after the command that started it exits.
func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
