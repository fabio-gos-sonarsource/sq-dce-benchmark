//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the child a process-group leader so we can signal the
// whole tree (mvn/gradle + forked JVMs, npm, node) at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the child's entire process group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
