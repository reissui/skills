package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden regenerates the service golden files when set: `go test -run
// TestServiceInstallRenders -update`.
var updateGolden = flag.Bool("update", false, "update golden files")

// fixedServiceParams are pinned so the rendered unit is byte-stable across
// machines (no real home path, binary, or repo leaks in).
func fixedServiceParams() serviceParams {
	return serviceParams{
		Label:      launchdLabel,
		BinaryPath: "/usr/local/bin/clexd",
		Repo:       "acme/widgets",
		User:       "clex",
		Home:       "/home/clex/.clex",
		EnvFile:    "/home/clex/.clex/clexd.env",
		LogPath:    "/home/clex/.clex/clexd.log",
		ErrLogPath: "/home/clex/.clex/clexd.err.log",
	}
}

func TestServiceInstallRenders(t *testing.T) {
	cases := []struct {
		goos   string
		golden string
	}{
		{"darwin", "com.reissui.clexd.plist"},
		{"linux", "clexd.service"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			got, err := renderServiceUnit(tc.goos, fixedServiceParams())
			if err != nil {
				t.Fatalf("render %s: %v", tc.goos, err)
			}
			goldenPath := filepath.Join("testdata", tc.golden+".golden")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("%s unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", tc.goos, got, want)
			}
		})
	}
}

// TestServiceInstallLoadsMocked: install renders the unit and calls the injected
// serviceManager (load is mocked) — no init system is touched.
func TestServiceInstallLoadsMocked(t *testing.T) {
	e := newTestEnv(t)
	e.goos = "linux"
	fs := e.service.(*fakeService)
	code := run(e, []string{"service", "install", "--repo", "acme/widgets", "--binary", "/usr/local/bin/clexd"})
	if code != exitOK {
		t.Fatalf("service install exit = %d, want 0 (stderr: %s)", code, errBuf(e))
	}
	if fs.installCalls != 1 {
		t.Fatalf("expected 1 install call, got %d", fs.installCalls)
	}
	if fs.lastGOOS != "linux" {
		t.Errorf("install goos = %q, want linux", fs.lastGOOS)
	}
	if !strings.Contains(fs.lastContent, "ExecStart=/usr/local/bin/clexd --repo acme/widgets") {
		t.Errorf("rendered unit missing ExecStart; got:\n%s", fs.lastContent)
	}
	if !strings.HasSuffix(fs.lastPath, "clexd.service") {
		t.Errorf("linux unit path = %q, want …/clexd.service", fs.lastPath)
	}
}

func TestServiceInstallDarwinPath(t *testing.T) {
	e := newTestEnv(t)
	e.goos = "darwin"
	fs := e.service.(*fakeService)
	code := run(e, []string{"service", "install", "--repo", "acme/widgets"})
	if code != exitOK {
		t.Fatalf("service install exit = %d, want 0", code)
	}
	if !strings.Contains(fs.lastPath, "LaunchAgents") || !strings.HasSuffix(fs.lastPath, ".plist") {
		t.Errorf("darwin unit path = %q, want …/LaunchAgents/….plist", fs.lastPath)
	}
	if !strings.Contains(fs.lastContent, "com.reissui.clexd") {
		t.Errorf("plist missing label; got:\n%s", fs.lastContent)
	}
}

func TestServiceStatusAndUninstall(t *testing.T) {
	e := newTestEnv(t)
	e.goos = "linux"
	fs := e.service.(*fakeService)
	fs.loaded = true

	if code := run(e, []string{"service", "status"}); code != exitOK {
		t.Fatalf("service status exit = %d, want 0", code)
	}
	if !strings.Contains(outBuf(e).String(), "loaded") {
		t.Errorf("status should say loaded; got: %s", outBuf(e))
	}

	outBuf(e).Reset()
	if code := run(e, []string{"service", "uninstall"}); code != exitOK {
		t.Fatalf("service uninstall exit = %d, want 0", code)
	}
	if fs.uninstallCalls != 1 {
		t.Fatalf("expected 1 uninstall call, got %d", fs.uninstallCalls)
	}
}

func TestServiceStatusJSON(t *testing.T) {
	e := newTestEnv(t)
	e.goos = "linux"
	e.service.(*fakeService).loaded = true
	code := run(e, []string{"service", "status", "--json"})
	if code != exitOK {
		t.Fatalf("service status --json exit = %d, want 0", code)
	}
	var got struct {
		OK     bool `json:"ok"`
		Loaded bool `json:"loaded"`
	}
	if err := json.Unmarshal(outBuf(e).Bytes(), &got); err != nil {
		t.Fatalf("service status --json invalid: %v\n%s", err, outBuf(e))
	}
	if !got.OK || !got.Loaded {
		t.Fatalf("unexpected status json: %+v", got)
	}
}

func TestServiceInstallUnsupportedPlatform(t *testing.T) {
	e := newTestEnv(t)
	e.goos = "windows"
	code := run(e, []string{"service", "install", "--repo", "acme/widgets"})
	if code != exitError {
		t.Fatalf("service install on windows: exit = %d, want 1", code)
	}
}
