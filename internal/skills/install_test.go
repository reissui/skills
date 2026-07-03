package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubFetcher returns canned bytes per skill name, so tests never touch the
// network (spec: injectable network step). It also records how many fetches ran.
type stubFetcher struct {
	byName map[string][]byte
	err    error
	calls  int
}

func (f *stubFetcher) Fetch(s VendoredSkill) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.byName[s.Name]
	if !ok {
		return nil, fmt.Errorf("stub has no content for %q", s.Name)
	}
	return data, nil
}

// fullStub returns a fetcher with deterministic content for every vendored skill.
func fullStub() *stubFetcher {
	m := map[string][]byte{}
	for _, s := range vendoredSkills {
		m[s.Name] = []byte("---\nname: " + s.Name + "\ndescription: vendored\n---\ncontent for " + s.Name + "\n")
	}
	return &stubFetcher{byName: m}
}

// TestInstallBundledWritesLockfile: with a stubbed fetcher, bundled skills are
// installed, vendored skills are written, and the lockfile pins URL + version +
// sha256 for each vendored skill (acceptance criteria 4 and 5).
func TestInstallBundledWritesLockfile(t *testing.T) {
	root := t.TempDir()
	fetch := fullStub()

	if err := InstallBundled(root, InstallOptions{Fetcher: fetch}); err != nil {
		t.Fatalf("InstallBundled: %v", err)
	}

	// Bundled clex-authored skills present on disk.
	for _, name := range []string{"clex-plan", "clex-issue-lint"} {
		if _, err := os.Stat(filepath.Join(root, name, "SKILL.md")); err != nil {
			t.Errorf("bundled skill %s not installed: %v", name, err)
		}
	}
	// Vendored skills present on disk.
	for _, s := range vendoredSkills {
		if _, err := os.Stat(filepath.Join(root, s.Name, "SKILL.md")); err != nil {
			t.Errorf("vendored skill %s not installed: %v", s.Name, err)
		}
	}
	if fetch.calls != len(vendoredSkills) {
		t.Errorf("fetch called %d times, want %d", fetch.calls, len(vendoredSkills))
	}

	// Lockfile pins every vendored skill with a non-empty sha256, url, version.
	lf, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(lf.Skills) != len(vendoredSkills) {
		t.Fatalf("lockfile has %d entries, want %d", len(lf.Skills), len(vendoredSkills))
	}
	for _, e := range lf.Skills {
		if e.URL == "" || e.Version == "" || len(e.SHA256) != 64 {
			t.Errorf("lock entry %q not fully pinned: %+v", e.Name, e)
		}
	}
	// The pinned hash must match the content actually written.
	byName := ExpectedFromLockfile(lf)
	for _, s := range vendoredSkills {
		data, err := os.ReadFile(filepath.Join(root, s.Name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256Hex(data); got != byName[s.Name] {
			t.Errorf("%s: on-disk hash %s != lockfile pin %s", s.Name, got, byName[s.Name])
		}
	}
}

// TestInstallBundledSkipFetch: with no fetcher, bundled skills still install and
// no lockfile is written (acceptance criterion 4 — skipping fetch still installs
// bundled skills).
func TestInstallBundledSkipFetch(t *testing.T) {
	root := t.TempDir()

	if err := InstallBundled(root, InstallOptions{Fetcher: nil}); err != nil {
		t.Fatalf("InstallBundled (no fetch): %v", err)
	}
	for _, name := range []string{"clex-plan", "clex-issue-lint"} {
		if _, err := os.Stat(filepath.Join(root, name, "SKILL.md")); err != nil {
			t.Errorf("bundled skill %s not installed without fetch: %v", name, err)
		}
	}
	// No vendored skills, no lockfile.
	if _, err := os.Stat(filepath.Join(root, lockFileName)); !os.IsNotExist(err) {
		t.Errorf("no lockfile expected when fetch skipped, stat err=%v", err)
	}
	for _, s := range vendoredSkills {
		if _, err := os.Stat(filepath.Join(root, s.Name)); !os.IsNotExist(err) {
			t.Errorf("vendored skill %s should not exist when fetch skipped", s.Name)
		}
	}
}

// TestInstallBundledHashMismatchFails: when a fetched skill's content does not
// match the pinned sha256, the install fails with a clear, wrapped error
// (acceptance criterion 5).
func TestInstallBundledHashMismatchFails(t *testing.T) {
	root := t.TempDir()
	fetch := fullStub()

	// Pin an expected hash that cannot match the stub's content.
	expected := map[string]string{
		"to-prd": "deadbeef" + "00000000000000000000000000000000000000000000000000000000",
	}

	err := InstallBundled(root, InstallOptions{Fetcher: fetch, Expected: expected})
	if err == nil {
		t.Fatal("expected hash-mismatch error, got nil")
	}
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("error should wrap ErrHashMismatch, got: %v", err)
	}
	// The message should name the skill and mention re-pinning, so it is clear.
	msg := err.Error()
	for _, want := range []string{"to-prd", "re-pin"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	// No lockfile written on failure (fetched set not committed).
	if _, statErr := os.Stat(filepath.Join(root, lockFileName)); !os.IsNotExist(statErr) {
		t.Errorf("lockfile should not be written on hash mismatch, stat err=%v", statErr)
	}
}

// TestInstallBundledRepinAccepts: when the expected hash matches (as it would
// after re-pinning), the install proceeds. This is the trust-on-first-use and
// re-pin path.
func TestInstallBundledRepinAccepts(t *testing.T) {
	root := t.TempDir()
	fetch := fullStub()

	// Compute the true hash for one skill and pin exactly that.
	trueHash := sha256Hex(fetch.byName["to-prd"])
	expected := map[string]string{"to-prd": trueHash}

	if err := InstallBundled(root, InstallOptions{Fetcher: fetch, Expected: expected}); err != nil {
		t.Fatalf("matching pin should install cleanly: %v", err)
	}
	lf, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	byName := ExpectedFromLockfile(lf)
	if byName["to-prd"] != trueHash {
		t.Errorf("lockfile pin drifted: %s != %s", byName["to-prd"], trueHash)
	}
}

// TestInstallBundledFetchErrorPropagates: a fetch transport error aborts the
// install with a wrapped error.
func TestInstallBundledFetchErrorPropagates(t *testing.T) {
	root := t.TempDir()
	fetch := &stubFetcher{err: errors.New("boom")}

	err := InstallBundled(root, InstallOptions{Fetcher: fetch})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("fetch error should propagate, got: %v", err)
	}
}
