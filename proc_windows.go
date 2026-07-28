//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcessGroup starts the child in its own process group so it can be terminated
// as a unit.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessGroup terminates the child and its whole tree (mvn/gradle + child
// JVMs/npm) via taskkill, since Windows has no process-group signal.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
