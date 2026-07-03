//go:build unix

package codex

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group so the whole tree
// (codex plus any tool subprocesses it spawns) can be signalled together.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup sends SIGKILL to the child's entire process group. Signalling the
// negative pgid targets every process in the group, guaranteeing tool
// subprocesses die with the parent on cancellation.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fall back to killing just the leader if the group is gone.
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
