package main

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

// serviceTemplates holds the launchd/systemd unit templates. They are embedded
// (rather than read from deploy/, which is outside this issue's Touches globs) so
// the CLI is self-contained and the rendered output is golden-testable. The
// deploy/ copies remain the human-facing reference; these are the source of truth
// for `clex service install`. See PR notes for this deviation.
//
//go:embed templates/*.tmpl
var serviceTemplates embed.FS

// launchdLabel is the launchd job label for the clex daemon (macOS).
const launchdLabel = "com.reissui.clexd"

// serviceManager loads/unloads the OS service after the unit file is rendered
// and written. The real implementation shells to launchctl/systemctl; tests
// inject a recorder so `service install` renders and "loads" without touching
// the init system.
type serviceManager interface {
	// Install writes unit content to path and enables/loads the service.
	Install(goos, path, content string) error
	// Uninstall disables/unloads the service and removes its unit file.
	Uninstall(goos, path string) error
	// Status reports whether the service is currently loaded/running.
	Status(goos, path string) (loaded bool, detail string, err error)
}

// serviceParams are the values substituted into a unit template.
type serviceParams struct {
	Label      string // launchd job label
	BinaryPath string // path to the clexd binary
	Repo       string // managed repository, owner/name
	User       string // systemd service user
	Home       string // clex home directory (systemd ReadWritePaths)
	EnvFile    string // systemd EnvironmentFile path
	LogPath    string // launchd stdout log
	ErrLogPath string // launchd stderr log
}

// cmdService dispatches the service subcommands: install | uninstall | status.
func cmdService(e *env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(e.stderr, "usage: clex service <install|uninstall|status> [flags]")
		return exitProblem
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return cmdServiceInstall(e, rest)
	case "uninstall":
		return cmdServiceUninstall(e, rest)
	case "status":
		return cmdServiceStatus(e, rest)
	case "-h", "--help", "help":
		fmt.Fprintln(e.stdout, "clex service <install|uninstall|status> — manage the clexd OS service")
		return exitOK
	default:
		fmt.Fprintf(e.stderr, "clex service: unknown subcommand %q\n", sub)
		return exitProblem
	}
}

// cmdServiceInstall renders the platform unit and loads it. --repo and --binary
// override the detected values (tests pin them for stable goldens).
func cmdServiceInstall(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "service install", "render and load the clexd service unit")
	repoFlag := fs.String("repo", "", "managed repository owner/name")
	binaryFlag := fs.String("binary", "", "path to the clexd binary (defaults to <dir of clex>/clexd)")
	userFlag := fs.String("user", "clex", "systemd service user (Linux only)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	params, err := e.serviceParams(*repoFlag, *binaryFlag, *userFlag)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	content, err := renderServiceUnit(e.goos, params)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	path := e.serviceUnitPath()
	if err := e.service.Install(e.goos, path, content); err != nil {
		return fail(e, *jsonOut, "install service: %v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, map[string]any{"ok": true, "path": path, "goos": e.goos})
	}
	fmt.Fprintf(e.stdout, "installed clexd service at %s and loaded it.\n", path)
	return exitOK
}

// cmdServiceUninstall unloads and removes the unit.
func cmdServiceUninstall(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "service uninstall", "unload and remove the clexd service unit")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	path := e.serviceUnitPath()
	if err := e.service.Uninstall(e.goos, path); err != nil {
		return fail(e, *jsonOut, "uninstall service: %v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, map[string]any{"ok": true, "path": path})
	}
	fmt.Fprintf(e.stdout, "uninstalled clexd service (%s).\n", path)
	return exitOK
}

// cmdServiceStatus reports whether the service is loaded.
func cmdServiceStatus(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "service status", "report whether the clexd service is loaded")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	path := e.serviceUnitPath()
	loaded, detail, err := e.service.Status(e.goos, path)
	if err != nil {
		return fail(e, *jsonOut, "service status: %v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, map[string]any{"ok": true, "loaded": loaded, "detail": detail, "path": path})
	}
	state := "not loaded"
	if loaded {
		state = "loaded"
	}
	if detail != "" {
		fmt.Fprintf(e.stdout, "clexd service: %s (%s)\n", state, detail)
	} else {
		fmt.Fprintf(e.stdout, "clexd service: %s\n", state)
	}
	return exitOK
}

// serviceParams builds the template values, filling defaults for the binary path
// (sibling clexd next to this clex binary), repo (from the git origin), home, and
// log/env paths.
func (e *env) serviceParams(repoFlag, binaryFlag, user string) (serviceParams, error) {
	repo := strings.TrimSpace(repoFlag)
	if repo == "" {
		if r, ok := e.configuredRepo(); ok {
			repo = r
		}
	}
	if repo == "" {
		return serviceParams{}, fmt.Errorf("no repository: pass --repo owner/name")
	}
	binary := strings.TrimSpace(binaryFlag)
	if binary == "" {
		binary = e.defaultDaemonBinary()
	}
	return serviceParams{
		Label:      launchdLabel,
		BinaryPath: binary,
		Repo:       repo,
		User:       user,
		Home:       e.home,
		EnvFile:    filepath.Join(e.home, "clexd.env"),
		LogPath:    filepath.Join(e.home, "clexd.log"),
		ErrLogPath: filepath.Join(e.home, "clexd.err.log"),
	}, nil
}

// renderServiceUnit renders the systemd or launchd template for goos.
func renderServiceUnit(goos string, p serviceParams) (string, error) {
	name := serviceTemplateName(goos)
	if name == "" {
		return "", fmt.Errorf("unsupported platform %q for service install (want darwin or linux)", goos)
	}
	tmpl, err := template.ParseFS(serviceTemplates, "templates/"+name)
	if err != nil {
		return "", fmt.Errorf("parse service template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", fmt.Errorf("render service template: %w", err)
	}
	return buf.String(), nil
}

// serviceTemplateName maps an OS to its unit template file name.
func serviceTemplateName(goos string) string {
	switch goos {
	case "darwin":
		return "com.reissui.clexd.plist.tmpl"
	case "linux":
		return "clexd.service.tmpl"
	default:
		return ""
	}
}

// serviceUnitPath is where the rendered unit is written for the platform: a
// per-user LaunchAgent on macOS, /etc/systemd/system on Linux.
func (e *env) serviceUnitPath() string {
	switch e.goos {
	case "darwin":
		return filepath.Join(e.home, "LaunchAgents", launchdLabel+".plist")
	case "linux":
		return "/etc/systemd/system/clexd.service"
	default:
		return filepath.Join(e.home, "clexd.service")
	}
}
