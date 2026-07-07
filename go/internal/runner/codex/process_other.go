//go:build !unix

package codex

import "os/exec"

// setProcessGroup is a no-op on platforms without POSIX process groups; the
// exec.CommandContext default (kill the leader) applies.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup falls back to killing just the child process on non-unix platforms.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
