package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// runtimeGOOS returns the build's OS. Wrapped so tests can override env.goos
// without touching the runtime.
func runtimeGOOS() string { return runtime.GOOS }

// defaultDaemonBinary guesses the clexd path as a sibling of the running clex
// binary (they ship together), falling back to /usr/local/bin/clexd.
func (e *env) defaultDaemonBinary() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "clexd")
	}
	return "/usr/local/bin/clexd"
}

// realService is the production serviceManager. It writes the unit file (0644,
// creating parent dirs) and loads it via launchctl (darwin) or systemctl
// (linux). It is never exercised by unit tests, which inject a fake.
type realService struct{}

func (realService) Install(goos, path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write unit %s: %w", path, err)
	}
	switch goos {
	case "darwin":
		return runCmd("launchctl", "load", "-w", path)
	case "linux":
		if err := runCmd("systemctl", "daemon-reload"); err != nil {
			return err
		}
		return runCmd("systemctl", "enable", "--now", filepath.Base(path))
	default:
		return fmt.Errorf("unsupported platform %q", goos)
	}
}

func (realService) Uninstall(goos, path string) error {
	switch goos {
	case "darwin":
		_ = runCmd("launchctl", "unload", "-w", path)
	case "linux":
		_ = runCmd("systemctl", "disable", "--now", filepath.Base(path))
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit %s: %w", path, err)
	}
	return nil
}

func (realService) Status(goos, path string) (bool, string, error) {
	switch goos {
	case "darwin":
		out, _ := exec.Command("launchctl", "list").Output()
		loaded := strings.Contains(string(out), launchdLabel)
		return loaded, "", nil
	case "linux":
		out, _ := exec.Command("systemctl", "is-active", filepath.Base(path)).Output()
		state := strings.TrimSpace(string(out))
		return state == "active", state, nil
	default:
		return false, "", fmt.Errorf("unsupported platform %q", goos)
	}
}

// runCmd executes a command, wrapping a failure with its combined output for a
// legible error.
func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
