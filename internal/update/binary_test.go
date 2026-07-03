package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- helpers ---

// sum returns the lower-hex sha256 of b.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// targetAssetName is the platform archive name for a version, matching
// assetNames (goreleaser layout).
func targetAssetName(v string) string {
	return fmt.Sprintf("clex_%s_%s_%s.tar.gz", normalizeTag(v), runtime.GOOS, runtime.GOARCH)
}

// releaseServer spins up an httptest server that serves a "latest release" JSON
// document plus the named assets. It returns the server and a ReleaseChecker
// pointed at it. assets maps asset filename → bytes; the JSON advertises each
// asset with a browser_download_url on this same server.
func releaseServer(t *testing.T, tag string, assets map[string][]byte) (*httptest.Server, *ReleaseChecker) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// asset bytes
	for name, body := range assets {
		body := body
		mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		})
	}
	// latest release JSON
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[`, tag)
		first := true
		for name := range assets {
			if !first {
				fmt.Fprint(w, ",")
			}
			first = false
			fmt.Fprintf(w, `{"name":%q,"browser_download_url":%q}`, name, srv.URL+"/dl/"+name)
		}
		fmt.Fprint(w, "]}")
	})
	return srv, &ReleaseChecker{HTTP: srv.Client(), LatestURL: srv.URL + "/latest"}
}

// --- Decide ---

func TestDecide(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		tag         string
		auto        string
		wantBump    Bump
		wantStage   bool
		wantConfirm bool
	}{
		{"patch auto-stages", "0.3.0", "v0.3.1", "patch", BumpPatch, true, false},
		{"patch off confirms", "0.3.0", "v0.3.1", "off", BumpPatch, false, true},
		{"minor confirms under patch", "0.3.0", "v0.4.0", "patch", BumpMinor, false, true},
		{"major confirms under patch", "0.3.0", "v1.0.0", "patch", BumpMajor, false, true},
		{"same is noop", "0.3.1", "v0.3.1", "patch", BumpNone, false, false},
		{"older is noop", "0.4.0", "v0.3.1", "patch", BumpNone, false, false},
		{"prerelease latest below final current is noop", "0.4.0", "v0.4.0-rc1", "patch", BumpNone, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, err := Decide(tt.current, Release{Tag: tt.tag}, tt.auto)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if dec.Bump != tt.wantBump {
				t.Errorf("bump = %q, want %q", dec.Bump, tt.wantBump)
			}
			if dec.AutoStage != tt.wantStage {
				t.Errorf("AutoStage = %v, want %v", dec.AutoStage, tt.wantStage)
			}
			gotConfirm := dec.Proposal != nil
			if gotConfirm != tt.wantConfirm {
				t.Errorf("confirm(Proposal!=nil) = %v, want %v", gotConfirm, tt.wantConfirm)
			}
			if gotConfirm {
				if dec.Proposal.Kind != KindReleaseConfirm {
					t.Errorf("Proposal.Kind = %q, want %q", dec.Proposal.Kind, KindReleaseConfirm)
				}
				if dec.Proposal.Release == nil || dec.Proposal.Release.Bump != tt.wantBump {
					t.Errorf("Proposal.Release.Bump wrong: %+v", dec.Proposal.Release)
				}
			}
		})
	}
}

func TestDecide_badVersions(t *testing.T) {
	if _, err := Decide("not-a-version", Release{Tag: "v1.0.0"}, "patch"); err == nil {
		t.Error("want error for bad current version")
	}
	if _, err := Decide("1.0.0", Release{Tag: "garbage"}, "patch"); err == nil {
		t.Error("want error for bad release tag")
	}
}

// --- ReleaseChecker.Latest ---

func TestReleaseChecker_Latest(t *testing.T) {
	_, chk := releaseServer(t, "v0.4.0", map[string][]byte{
		"checksums.txt": []byte("deadbeef  clex_0.4.0_linux_amd64.tar.gz\n"),
	})
	rel, err := chk.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Tag != "v0.4.0" {
		t.Errorf("tag = %q, want v0.4.0", rel.Tag)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(rel.Assets))
	}
}

func TestReleaseChecker_Latest_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	chk := &ReleaseChecker{HTTP: srv.Client(), LatestURL: srv.URL}
	if _, err := chk.Latest(context.Background()); err == nil {
		t.Error("want error on HTTP 500")
	}
}

// --- checksum verification (before staging) ---

func TestVerifyChecksum(t *testing.T) {
	data := []byte("the-new-binary-archive")
	name := "clex_0.4.0_x.tar.gz"
	good := map[string]string{name: sum(data)}
	if err := verifyChecksum(name, data, good); err != nil {
		t.Errorf("good checksum should verify: %v", err)
	}
	// wrong digest
	bad := map[string]string{name: sum([]byte("something-else"))}
	if err := verifyChecksum(name, data, bad); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("mismatch should be ErrChecksumMismatch, got %v", err)
	}
	// not listed
	if err := verifyChecksum(name, data, map[string]string{}); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("unlisted asset should be ErrChecksumMismatch, got %v", err)
	}
}

func TestParseChecksums(t *testing.T) {
	manifest := []byte("ABCD  file-a.tar.gz\n\n0011  file-b.zip\nmalformed-line\n")
	got := parseChecksums(manifest)
	if got["file-a.tar.gz"] != "abcd" { // lower-cased
		t.Errorf("file-a = %q, want abcd", got["file-a.tar.gz"])
	}
	if got["file-b.zip"] != "0011" {
		t.Errorf("file-b = %q, want 0011", got["file-b.zip"])
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (malformed skipped)", len(got))
	}
}

// --- Stager: stage / apply / rollback on temp dirs ---

// tempBinary writes a fake current binary into a temp dir and returns a Stager
// pointed at it plus the path.
func tempBinary(t *testing.T, content string) (*Stager, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "clex")
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	return &Stager{BinPath: bin}, bin
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestStager_StageVerified_thenApply(t *testing.T) {
	st, bin := tempBinary(t, "OLD")
	newBytes := []byte("NEW-BINARY")
	name := "clex_1.0.0.tar.gz"
	sums := map[string]string{name: sum(newBytes)}

	if err := st.StageVerified(name, newBytes, sums); err != nil {
		t.Fatalf("StageVerified: %v", err)
	}
	if !st.Staged() {
		t.Fatal("expected staged binary present")
	}
	// live binary unchanged until Apply
	if got := readFile(t, bin); got != "OLD" {
		t.Errorf("live binary changed before Apply: %q", got)
	}
	if err := st.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, bin); got != "NEW-BINARY" {
		t.Errorf("after Apply live = %q, want NEW-BINARY", got)
	}
	// previous binary retained
	if got := readFile(t, st.backupPath()); got != "OLD" {
		t.Errorf("backup = %q, want OLD", got)
	}
}

func TestStager_StageVerified_mismatchAbortsStaging(t *testing.T) {
	st, bin := tempBinary(t, "OLD")
	newBytes := []byte("NEW-BINARY")
	name := "clex_1.0.0.tar.gz"
	// published digest is for different bytes → mismatch
	sums := map[string]string{name: sum([]byte("tampered"))}

	err := st.StageVerified(name, newBytes, sums)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	if st.Staged() {
		t.Error("nothing should be staged on checksum mismatch")
	}
	if got := readFile(t, bin); got != "OLD" {
		t.Errorf("live binary must be untouched, got %q", got)
	}
}

func TestStager_Rollback(t *testing.T) {
	st, bin := tempBinary(t, "OLD")
	newBytes := []byte("NEW")
	name := "n.tar.gz"
	if err := st.StageVerified(name, newBytes, map[string]string{name: sum(newBytes)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := st.Apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readFile(t, bin); got != "NEW" {
		t.Fatalf("precondition: live should be NEW, got %q", got)
	}
	// Roll back to OLD.
	if err := st.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, bin); got != "OLD" {
		t.Errorf("after rollback live = %q, want OLD", got)
	}
}

func TestStager_Rollback_noBackup(t *testing.T) {
	st, _ := tempBinary(t, "OLD")
	if err := st.Rollback(); !errors.Is(err, ErrNoBackup) {
		t.Errorf("want ErrNoBackup, got %v", err)
	}
}

func TestStager_Apply_nothingStaged(t *testing.T) {
	st, _ := tempBinary(t, "OLD")
	if err := st.Apply(); err == nil {
		t.Error("Apply with nothing staged should error")
	}
}

// --- Updater.Run end to end (in-memory HTTP + temp binary) ---

// applyNow is an ApplyHook that runs apply immediately (simulating a quiesced
// daemon). It records that it was called.
func applyNow(called *bool) ApplyHook {
	return func(ctx context.Context, apply func() error) error {
		*called = true
		return apply()
	}
}

// applyDefer is an ApplyHook that never runs apply (daemon busy); records call.
func applyDefer(called *bool) ApplyHook {
	return func(ctx context.Context, apply func() error) error {
		*called = true
		return nil // deferred
	}
}

func TestUpdater_Run_patchAutoStagesAndApplies(t *testing.T) {
	tag := "v0.4.1"
	archive := []byte("BRAND-NEW-CLEX")
	tName := targetAssetName(tag)
	assets := map[string][]byte{
		tName:           archive,
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n", sum(archive), tName)),
	}
	srv, chk := releaseServer(t, tag, assets)
	st, bin := tempBinary(t, "OLD-CLEX")

	hookCalled := false
	u := &Updater{
		Current:    "0.4.0",
		Auto:       "patch",
		Checker:    chk,
		Downloader: &Downloader{HTTP: srv.Client()},
		Stager:     st,
		Apply:      applyNow(&hookCalled),
	}
	res, err := u.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Staged {
		t.Error("patch under auto=patch should stage")
	}
	if !hookCalled {
		t.Error("apply hook should be called")
	}
	if !res.Applied {
		t.Error("applyNow hook should have applied")
	}
	if got := readFile(t, bin); got != "BRAND-NEW-CLEX" {
		t.Errorf("live binary = %q, want BRAND-NEW-CLEX", got)
	}
}

func TestUpdater_Run_minorProposesNoStage(t *testing.T) {
	tag := "v0.5.0"
	archive := []byte("NEW")
	tName := targetAssetName(tag)
	assets := map[string][]byte{
		tName:           archive,
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n", sum(archive), tName)),
	}
	srv, chk := releaseServer(t, tag, assets)
	st, bin := tempBinary(t, "OLD")

	u := &Updater{
		Current:    "0.4.0",
		Auto:       "patch",
		Checker:    chk,
		Downloader: &Downloader{HTTP: srv.Client()},
		Stager:     st,
	}
	res, err := u.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Staged {
		t.Error("minor bump must not auto-stage")
	}
	if len(res.Proposals) != 1 || res.Proposals[0].Kind != KindReleaseConfirm {
		t.Fatalf("want one release-confirm proposal, got %+v", res.Proposals)
	}
	if got := readFile(t, bin); got != "OLD" {
		t.Errorf("binary must be untouched on a proposal, got %q", got)
	}
}

func TestUpdater_Run_checksumMismatchAborts(t *testing.T) {
	tag := "v0.4.1"
	archive := []byte("REAL")
	tName := targetAssetName(tag)
	assets := map[string][]byte{
		tName: archive,
		// published sum is for different bytes → mismatch on download
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n", sum([]byte("TAMPERED")), tName)),
	}
	srv, chk := releaseServer(t, tag, assets)
	st, bin := tempBinary(t, "OLD")

	hookCalled := false
	u := &Updater{
		Current:    "0.4.0",
		Auto:       "patch",
		Checker:    chk,
		Downloader: &Downloader{HTTP: srv.Client()},
		Stager:     st,
		Apply:      applyNow(&hookCalled),
	}
	_, err := u.Run(context.Background())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	if st.Staged() {
		t.Error("must not stage on mismatch")
	}
	if hookCalled {
		t.Error("apply hook must not run on mismatch")
	}
	if got := readFile(t, bin); got != "OLD" {
		t.Errorf("binary untouched expected, got %q", got)
	}
}

func TestUpdater_Run_deferredApply(t *testing.T) {
	tag := "v0.4.1"
	archive := []byte("NEW")
	tName := targetAssetName(tag)
	assets := map[string][]byte{
		tName:           archive,
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n", sum(archive), tName)),
	}
	srv, chk := releaseServer(t, tag, assets)
	st, bin := tempBinary(t, "OLD")

	called := false
	u := &Updater{
		Current: "0.4.0", Auto: "patch",
		Checker: chk, Downloader: &Downloader{HTTP: srv.Client()}, Stager: st,
		Apply: applyDefer(&called),
	}
	res, err := u.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Staged || !called {
		t.Fatalf("expected staged+hook called, got staged=%v called=%v", res.Staged, called)
	}
	if res.Applied {
		t.Error("deferred hook should leave Applied=false")
	}
	// Binary not swapped yet; staged file waits.
	if got := readFile(t, bin); got != "OLD" {
		t.Errorf("live binary should still be OLD until a quiescent apply, got %q", got)
	}
	if !st.Staged() {
		t.Error("staged binary should remain for the next quiescent tick")
	}
}

func TestUpdater_ApplyConfirmed(t *testing.T) {
	// A minor bump the owner confirmed: ApplyConfirmed stages + applies despite
	// auto policy not being "patch".
	tag := "v0.5.0"
	archive := []byte("CONFIRMED-NEW")
	tName := targetAssetName(tag)
	assets := map[string][]byte{
		tName:           archive,
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n", sum(archive), tName)),
	}
	srv, chk := releaseServer(t, tag, assets)
	st, bin := tempBinary(t, "OLD")
	latest, err := chk.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	called := false
	u := &Updater{
		Current: "0.4.0", Auto: "off", // not patch — confirm path
		Checker: chk, Downloader: &Downloader{HTTP: srv.Client()}, Stager: st,
		Apply: applyNow(&called),
	}
	res, err := u.ApplyConfirmed(context.Background(), latest)
	if err != nil {
		t.Fatalf("ApplyConfirmed: %v", err)
	}
	if !res.Staged || !res.Applied {
		t.Fatalf("expected staged+applied, got %+v", res)
	}
	if got := readFile(t, bin); got != "CONFIRMED-NEW" {
		t.Errorf("live = %q, want CONFIRMED-NEW", got)
	}
}
