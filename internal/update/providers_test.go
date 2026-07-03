package update

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeCmdRunner records every command and returns scripted output/errors keyed
// by the command name (argv[0]). No process is ever spawned.
type fakeCmdRunner struct {
	calls   [][]string        // full argv of each Run
	outputs map[string]string // argv[0] → stdout
	errs    map[string]error  // argv[0] → error
}

func newFakeCmdRunner() *fakeCmdRunner {
	return &fakeCmdRunner{outputs: map[string]string{}, errs: map[string]error{}}
}

func (f *fakeCmdRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.outputs[name], f.errs[name]
}

// ran reports whether an argv[0] was invoked and returns its full argv.
func (f *fakeCmdRunner) argvFor(name string) ([]string, bool) {
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == name {
			return c, true
		}
	}
	return nil, false
}

// fakeProber records that Probe was called (post-update re-probe).
type fakeProber struct{ probes int }

func (f *fakeProber) Probe(context.Context) { f.probes++ }

// fakePins records the version-pin recheck.
type fakePins struct {
	checks int
	err    error
}

func (f *fakePins) CheckPins(context.Context) error { f.checks++; return f.err }

func TestUpdateClaude_recordsCommandAndProbes(t *testing.T) {
	run := newFakeCmdRunner()
	prober := &fakeProber{}
	pins := &fakePins{}
	pu := &ProviderUpdater{Runner: run, Registry: prober, Pins: pins}

	res, err := pu.UpdateClaude(context.Background())
	if err != nil {
		t.Fatalf("UpdateClaude: %v", err)
	}
	if !res.Updated {
		t.Error("expected Updated=true")
	}
	argv, ok := run.argvFor("claude")
	if !ok {
		t.Fatal("claude updater was not invoked")
	}
	if !reflect.DeepEqual(argv, []string{"claude", "update"}) {
		t.Errorf("argv = %v, want [claude update]", argv)
	}
	// Post-update probe + pin check must fire.
	if prober.probes != 1 {
		t.Errorf("post-update probes = %d, want 1", prober.probes)
	}
	if pins.checks != 1 {
		t.Errorf("post-update pin checks = %d, want 1", pins.checks)
	}
}

func TestUpdateCodex_pkgManagerSelection(t *testing.T) {
	tests := []struct {
		name     string
		pkg      PkgManager
		wantArgv []string
	}{
		{"brew", PkgBrew, []string{"brew", "upgrade", "codex"}},
		{"npm", PkgNpm, []string{"npm", "install", "-g", "@openai/codex@latest"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := newFakeCmdRunner()
			prober := &fakeProber{}
			pu := &ProviderUpdater{Runner: run, CodexPkg: tt.pkg, Registry: prober}

			res, err := pu.UpdateCodex(context.Background())
			if err != nil {
				t.Fatalf("UpdateCodex: %v", err)
			}
			if !res.Updated {
				t.Error("expected Updated=true")
			}
			argv, ok := run.argvFor(tt.wantArgv[0])
			if !ok {
				t.Fatalf("%s not invoked", tt.wantArgv[0])
			}
			if !reflect.DeepEqual(argv, tt.wantArgv) {
				t.Errorf("argv = %v, want %v", argv, tt.wantArgv)
			}
			if prober.probes != 1 {
				t.Errorf("post-update probes = %d, want 1", prober.probes)
			}
		})
	}
}

func TestUpdateCodex_unknownPkgManagerErrors(t *testing.T) {
	run := newFakeCmdRunner()
	pu := &ProviderUpdater{Runner: run, CodexPkg: PkgManager("apt")}
	if _, err := pu.UpdateCodex(context.Background()); err == nil {
		t.Error("unknown package manager should error")
	}
	if len(run.calls) != 0 {
		t.Errorf("no command should run for an unknown pkg manager, got %v", run.calls)
	}
}

func TestUpdate_failedUpdaterDoesNotProbe(t *testing.T) {
	run := newFakeCmdRunner()
	run.errs["claude"] = errors.New("network down")
	prober := &fakeProber{}
	pu := &ProviderUpdater{Runner: run, Registry: prober}

	res, err := pu.UpdateClaude(context.Background())
	if err == nil {
		t.Fatal("expected error from failed updater")
	}
	if res.Updated {
		t.Error("Updated should be false on failure")
	}
	if prober.probes != 0 {
		t.Errorf("no re-probe on failed update, got %d", prober.probes)
	}
}

func TestUpdateAll_oneProviderFailsOtherSucceeds(t *testing.T) {
	run := newFakeCmdRunner()
	run.errs["claude"] = errors.New("claude broke") // claude fails
	prober := &fakeProber{}
	pu := &ProviderUpdater{Runner: run, CodexPkg: PkgBrew, Registry: prober}

	results, err := pu.UpdateAll(context.Background())
	if err != nil {
		t.Fatalf("UpdateAll should succeed if any provider updates: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	// codex should have succeeded and triggered a probe.
	if prober.probes != 1 {
		t.Errorf("expected 1 probe (from codex only), got %d", prober.probes)
	}
}

func TestUpdateClaude_noRunnerErrors(t *testing.T) {
	pu := &ProviderUpdater{}
	if _, err := pu.UpdateClaude(context.Background()); err == nil {
		t.Error("missing CmdRunner should error")
	}
}
