package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
)

// Layer 1 of self-update: the clex binary itself. The flow, per spec
// (Self-update layer 1):
//
//   check GitHub Releases → compare semver → decide (auto-stage patch vs confirm
//   minor/major vs no-op) → on apply: download asset, VERIFY its sha256 against
//   the published checksums file BEFORE staging, stage next to the current
//   binary, retain the previous binary, then hand the atomic swap to an injected
//   apply-hook that runs it only when the daemon is quiesced.
//
// Everything that touches the network or the filesystem is injected, so tests
// use an in-memory HTTP transport, checksum fixtures, and t.TempDir(). This
// package never imports internal/daemon (that would create an import cycle); the
// daemon satisfies ApplyHook at wiring time.

// ApplyHook runs apply only when it is safe to swap the binary (no active
// runners, no open gates). The daemon's (*Daemon).ApplyWhenQuiesced satisfies
// this signature exactly, so wiring passes it directly; tests pass a fake that
// either invokes apply immediately or records that it was deferred. A hook may
// return before apply runs (deferred until the next quiescent tick); it returns
// apply's error if it did run.
type ApplyHook func(ctx context.Context, apply func() error) error

// Asset is one downloadable artifact attached to a release.
type Asset struct {
	// Name is the asset filename, e.g. "clex_0.4.0_darwin_arm64.tar.gz" or the
	// checksums manifest "checksums.txt".
	Name string
	// URL is where the asset bytes are fetched from (a browser_download_url in
	// GitHub's model; any URL the injected client can GET in tests).
	URL string
}

// Release is a published clex release, reduced to what layer 1 needs. It is
// decoded from the GitHub Releases API but the decode is isolated in
// ReleaseChecker so the rest of the package stays transport-agnostic.
type Release struct {
	// Tag is the release tag, e.g. "v0.4.0"; parsed with parseSemver.
	Tag string
	// Assets are the attached files, including the checksums manifest.
	Assets []Asset
}

// ReleaseChecker fetches the latest clex release from GitHub Releases over an
// injected http.Client. In tests the client's Transport is an in-memory double,
// so no live network is touched.
type ReleaseChecker struct {
	// HTTP is the client used for every request; nil means http.DefaultClient.
	HTTP *http.Client
	// LatestURL is the "latest release" endpoint. Defaults to the public GitHub
	// API for this repo; overridable in tests to point at an httptest server.
	LatestURL string
}

// defaultLatestURL is the GitHub Releases "latest" endpoint for this repo.
const defaultLatestURL = "https://api.github.com/repos/reissui/clex/releases/latest"

// httpClient returns the configured client or the stdlib default.
func (c *ReleaseChecker) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// ghRelease mirrors the subset of GitHub's release JSON we consume.
type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest fetches the newest published release. A non-2xx response or malformed
// body is an error; a well-formed release with an empty tag is also an error
// (there is nothing to compare against).
func (c *ReleaseChecker) Latest(ctx context.Context) (Release, error) {
	url := c.LatestURL
	if url == "" {
		url = defaultLatestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("update: build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("update: fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Release{}, fmt.Errorf("update: latest release returned HTTP %d", resp.StatusCode)
	}
	var gr ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return Release{}, fmt.Errorf("update: decode release: %w", err)
	}
	if strings.TrimSpace(gr.TagName) == "" {
		return Release{}, errors.New("update: latest release has no tag")
	}
	rel := Release{Tag: gr.TagName}
	for _, a := range gr.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return rel, nil
}

// Decision is the outcome of comparing the running version to the latest
// release: what (if anything) the caller should do.
type Decision struct {
	// Bump is the upgrade class from current→latest (BumpNone if same/older).
	Bump Bump
	// AutoStage is true when the engine may stage silently without a confirm —
	// i.e. a patch bump under update.auto="patch".
	AutoStage bool
	// Proposal is non-nil when the caller must ask the owner first (a minor/major
	// bump, or any bump when auto != "patch"). It is a *Proposal ready to surface.
	Proposal *Proposal
	// Latest is the release the decision is about (zero value if BumpNone).
	Latest Release
}

// autoPatch is the config.Update.Auto value that enables silent patch staging.
const autoPatch = "patch"

// AutoAllowsAutoStage reports whether the given config.Update.Auto value permits
// silent auto-staging of a patch release. Only "patch" does; "off", "", and any
// unrecognized value require an explicit confirm (spec: Self-update — patch
// releases auto-apply if update.auto = "patch"). This is the single point that
// interprets the policy string, so both the daily tick and `clex update` agree.
func AutoAllowsAutoStage(auto string) bool { return auto == autoPatch }

// Decide compares the running version string against a fetched release under the
// given auto policy (config.Update.Auto: "off"/"patch"/…). It performs no I/O.
//   - same-or-older latest → Bump=BumpNone, nothing to do.
//   - patch bump AND auto=="patch" → AutoStage=true (stage silently).
//   - otherwise (minor/major, or any bump when auto!="patch") → a
//     KindReleaseConfirm Proposal, AutoStage=false.
//
// An unparseable current or latest version is an error rather than a silent
// no-op, so a broken build/tag surfaces loudly.
func Decide(current string, latest Release, auto string) (Decision, error) {
	cur, err := parseSemver(current)
	if err != nil {
		return Decision{}, fmt.Errorf("update: current version: %w", err)
	}
	next, err := parseSemver(latest.Tag)
	if err != nil {
		return Decision{}, fmt.Errorf("update: release tag: %w", err)
	}
	bump := bumpBetween(cur, next)
	if bump == BumpNone {
		return Decision{Bump: BumpNone}, nil
	}
	d := Decision{Bump: bump, Latest: latest}
	if bump == BumpPatch && auto == autoPatch {
		d.AutoStage = true
		return d, nil
	}
	d.Proposal = &Proposal{
		Kind: KindReleaseConfirm,
		Message: fmt.Sprintf("clex %s available — update? [✓ yes] [changelog] [skip]",
			normalizeTag(latest.Tag)),
		Release: &ReleaseProposal{
			Current: normalizeTag(current),
			Latest:  normalizeTag(latest.Tag),
			Bump:    bump,
		},
	}
	return d, nil
}

// normalizeTag strips a leading "v" for display; parse errors fall back to the
// original string so display never panics on a weird tag.
func normalizeTag(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

// assetNames returns the release-asset name for the running GOOS/GOARCH and the
// checksums manifest name. clex releases follow goreleaser's default layout:
// per-target archives plus a single "checksums.txt" listing "<sha256>  <name>".
func assetNames(version string) (target, checksums string) {
	v := normalizeTag(version)
	// e.g. clex_0.4.0_darwin_arm64.tar.gz
	target = fmt.Sprintf("clex_%s_%s_%s.tar.gz", v, runtime.GOOS, runtime.GOARCH)
	return target, "checksums.txt"
}

// Downloader fetches asset bytes over an injected client. Split from
// ReleaseChecker so a download can be faked independently of release listing.
type Downloader struct {
	// HTTP is the client used for asset GETs; nil means http.DefaultClient.
	HTTP *http.Client
	// MaxBytes caps a single asset download to bound memory/disk on a hostile or
	// misconfigured release. Zero means defaultMaxAssetBytes.
	MaxBytes int64
}

// defaultMaxAssetBytes bounds a single asset download (256 MiB) so a bad release
// cannot exhaust disk. Generous for a Go binary archive; overridable in tests.
const defaultMaxAssetBytes = 256 << 20

func (d *Downloader) httpClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return http.DefaultClient
}

func (d *Downloader) maxBytes() int64 {
	if d.MaxBytes > 0 {
		return d.MaxBytes
	}
	return defaultMaxAssetBytes
}

// fetch GETs url and returns its body, capped at maxBytes.
func (d *Downloader) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build download request: %w", err)
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("update: download %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, d.maxBytes()+1))
	if err != nil {
		return nil, fmt.Errorf("update: read %s: %w", url, err)
	}
	if int64(len(b)) > d.maxBytes() {
		return nil, fmt.Errorf("update: asset %s exceeds %d-byte limit", url, d.maxBytes())
	}
	return b, nil
}

// parseChecksums parses a goreleaser-style checksums.txt: each non-blank line is
// "<hex-sha256>  <filename>" (two spaces, per sha256sum). It returns a map from
// filename to lower-hex digest. Malformed lines are skipped rather than fatal,
// but a completely empty manifest yields an empty map (verification of any asset
// then fails with "not listed").
func parseChecksums(manifest []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(manifest), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		name := fields[len(fields)-1]
		out[name] = sum
	}
	return out
}

// ErrChecksumMismatch is returned (wrapped) when a downloaded asset's sha256
// does not match the published checksums manifest. Staging is aborted; the
// current binary is untouched.
var ErrChecksumMismatch = errors.New("update: release asset checksum mismatch")

// verifyChecksum computes sha256(data) and compares it (case-insensitively) to
// the digest published for name in the manifest. An asset absent from the
// manifest is a mismatch (we never stage an unlisted asset).
func verifyChecksum(name string, data []byte, sums map[string]string) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%w: %q is not listed in checksums", ErrChecksumMismatch, name)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: %q got %s want %s", ErrChecksumMismatch, name, got, want)
	}
	return nil
}

// Stager owns the on-disk binary swap. It stages a verified new binary beside
// the current one, retains the previous binary for rollback, and performs the
// atomic replace. All paths live under a directory the caller controls (in tests
// a t.TempDir()); nothing here reaches the network.
type Stager struct {
	// BinPath is the absolute path of the running clex binary to be replaced.
	BinPath string
}

// staged/backup filenames live next to the target binary, so the rename that
// swaps them is atomic (same filesystem). ".staged" holds a verified new binary
// awaiting the quiescent swap; ".prev" holds the retained previous binary for
// --rollback.
func (s *Stager) stagedPath() string { return s.BinPath + ".staged" }
func (s *Stager) backupPath() string { return s.BinPath + ".prev" }

// Stage writes the verified new-binary bytes to the staging path (0755, so the
// eventual swap yields an executable) without touching the live binary. It is
// safe to call repeatedly; the last staged bytes win. Callers MUST have verified
// the checksum first — Stage does not re-verify (StageVerified is the checked
// entry point).
func (s *Stager) Stage(data []byte) error {
	if err := os.WriteFile(s.stagedPath(), data, 0o755); err != nil {
		return fmt.Errorf("update: write staged binary: %w", err)
	}
	return nil
}

// StageVerified verifies data against the manifest for the given asset name and,
// only on success, stages it. A mismatch returns an error wrapping
// ErrChecksumMismatch and stages nothing (spec: verify checksums BEFORE
// staging).
func (s *Stager) StageVerified(name string, data []byte, sums map[string]string) error {
	if err := verifyChecksum(name, data, sums); err != nil {
		return err
	}
	return s.Stage(data)
}

// Staged reports whether a staged binary is currently waiting to be applied.
func (s *Stager) Staged() bool {
	_, err := os.Stat(s.stagedPath())
	return err == nil
}

// Apply performs the atomic swap: it moves the current binary aside to the
// backup path (retained for rollback) and renames the staged binary into place.
// It is the callback handed to the ApplyHook and therefore runs only when the
// daemon is quiesced. Apply requires a staged binary; calling it with nothing
// staged is an error. On failure it attempts to restore the original so a
// crashed swap never leaves the install without a binary.
func (s *Stager) Apply() error {
	if !s.Staged() {
		return errors.New("update: nothing staged to apply")
	}
	// Retain the current binary as the rollback target. Rename within the same
	// dir is atomic and cheap.
	if err := os.Rename(s.BinPath, s.backupPath()); err != nil {
		return fmt.Errorf("update: back up current binary: %w", err)
	}
	if err := os.Rename(s.stagedPath(), s.BinPath); err != nil {
		// Swap failed after moving the original aside — put it back so the
		// install is never left binary-less.
		if rerr := os.Rename(s.backupPath(), s.BinPath); rerr != nil {
			return fmt.Errorf("update: install staged binary failed (%v) AND restore failed (%v)", err, rerr)
		}
		return fmt.Errorf("update: install staged binary: %w", err)
	}
	return nil
}

// ErrNoBackup is returned by Rollback when there is no retained previous binary
// to restore.
var ErrNoBackup = errors.New("update: no previous binary to roll back to")

// Rollback restores the retained previous binary, undoing the most recent
// Apply. It renames the current (new) binary out of the way to the staging path
// and moves the backup back into place, so a subsequent Apply could re-install
// the new one. With no backup present it returns ErrNoBackup and changes
// nothing. Drives `clex update --rollback` (spec: keeps the previous binary for
// clex update --rollback).
func (s *Stager) Rollback() error {
	if _, err := os.Stat(s.backupPath()); err != nil {
		return ErrNoBackup
	}
	// Move the current (new) binary aside so the backup can take the live path.
	// Keep it at the staging path so a re-apply is possible.
	if err := os.Rename(s.BinPath, s.stagedPath()); err != nil {
		return fmt.Errorf("update: set aside current binary for rollback: %w", err)
	}
	if err := os.Rename(s.backupPath(), s.BinPath); err != nil {
		// Restore failed; put the current binary back to avoid a binary-less
		// install.
		if rerr := os.Rename(s.stagedPath(), s.BinPath); rerr != nil {
			return fmt.Errorf("update: rollback failed (%v) AND restore of current failed (%v)", err, rerr)
		}
		return fmt.Errorf("update: restore previous binary: %w", err)
	}
	return nil
}

// Updater ties layer 1 together: it checks for a release, decides, and — when
// staging is warranted — downloads, verifies, stages, and hands the swap to the
// injected ApplyHook. Every dependency is a field so tests wire fakes and the
// daemon wires real implementations.
type Updater struct {
	// Current is the running binary's version (e.g. version.Version).
	Current string
	// Auto is config.Update.Auto ("off"/"patch"/…).
	Auto string
	// Checker fetches the latest release; required.
	Checker *ReleaseChecker
	// Downloader fetches asset + checksums bytes; required for staging.
	Downloader *Downloader
	// Stager owns the on-disk swap; required for staging/apply.
	Stager *Stager
	// Apply runs the swap when quiesced (the daemon's ApplyWhenQuiesced). If nil,
	// Run stages but does not apply (the daily tick will apply on a later pass).
	Apply ApplyHook
}

// Result reports what an update Run did, for the caller to log/surface.
type Result struct {
	// Decision is the release comparison outcome.
	Decision Decision
	// Staged is true if a verified binary was written to the staging path.
	Staged bool
	// Applied is true if the swap was handed to the apply-hook and it ran the
	// swap synchronously (a deferred hook leaves this false).
	Applied bool
	// Proposals collects anything the caller must surface (a release confirm when
	// the bump is larger than a patch or auto!="patch").
	Proposals []Proposal
}

// Run performs one layer-1 pass: check → decide → (auto-stage | propose | no-op)
// → optional apply. It never blocks on a human: a required confirmation is
// returned as a Proposal for the caller to surface, not awaited here. Staging
// verifies the asset checksum before writing (a mismatch aborts with an error
// wrapping ErrChecksumMismatch and leaves the live binary untouched).
func (u *Updater) Run(ctx context.Context) (Result, error) {
	var res Result
	if u.Checker == nil {
		return res, errors.New("update: Updater has no ReleaseChecker")
	}
	latest, err := u.Checker.Latest(ctx)
	if err != nil {
		return res, err
	}
	dec, err := Decide(u.Current, latest, u.Auto)
	if err != nil {
		return res, err
	}
	res.Decision = dec
	switch {
	case dec.Bump == BumpNone:
		return res, nil
	case dec.AutoStage:
		if err := u.stage(ctx, latest); err != nil {
			return res, err
		}
		res.Staged = true
	default:
		// Needs confirmation: surface the proposal, stage nothing yet.
		if dec.Proposal != nil {
			res.Proposals = append(res.Proposals, *dec.Proposal)
		}
		return res, nil
	}
	// Something was staged: try to apply it now if a hook is wired. The hook may
	// defer the swap until quiescent, in which case apply runs later and Applied
	// stays false for this pass.
	if u.Apply != nil {
		applied := false
		if err := u.Apply(ctx, func() error {
			if err := u.Stager.Apply(); err != nil {
				return err
			}
			applied = true
			return nil
		}); err != nil {
			return res, err
		}
		res.Applied = applied
	}
	return res, nil
}

// ApplyConfirmed downloads, verifies, stages, and applies a release the owner
// has just confirmed (the [✓ yes] path from a KindReleaseConfirm proposal). It
// is the entry point #17/#18 call after a confirm, bypassing the auto-stage
// gate. The swap still goes through the quiesce hook.
func (u *Updater) ApplyConfirmed(ctx context.Context, latest Release) (Result, error) {
	var res Result
	dec, err := Decide(u.Current, latest, autoPatch) // force-stage path
	if err != nil {
		return res, err
	}
	res.Decision = dec
	if dec.Bump == BumpNone {
		return res, nil
	}
	if err := u.stage(ctx, latest); err != nil {
		return res, err
	}
	res.Staged = true
	if u.Apply != nil {
		applied := false
		if err := u.Apply(ctx, func() error {
			if err := u.Stager.Apply(); err != nil {
				return err
			}
			applied = true
			return nil
		}); err != nil {
			return res, err
		}
		res.Applied = applied
	}
	return res, nil
}

// stage locates the target archive + checksums manifest in the release,
// downloads both, verifies the archive's sha256 against the manifest, and stages
// it — in that order, so an unverified byte is never written to disk.
func (u *Updater) stage(ctx context.Context, latest Release) error {
	if u.Downloader == nil || u.Stager == nil {
		return errors.New("update: staging requires a Downloader and a Stager")
	}
	targetName, sumsName := assetNames(latest.Tag)
	targetAsset, ok := findAsset(latest, targetName)
	if !ok {
		return fmt.Errorf("update: release %s has no asset %q for this platform", latest.Tag, targetName)
	}
	sumsAsset, ok := findAsset(latest, sumsName)
	if !ok {
		return fmt.Errorf("update: release %s has no checksums manifest %q", latest.Tag, sumsName)
	}
	sumsBytes, err := u.Downloader.fetch(ctx, sumsAsset.URL)
	if err != nil {
		return err
	}
	sums := parseChecksums(sumsBytes)
	data, err := u.Downloader.fetch(ctx, targetAsset.URL)
	if err != nil {
		return err
	}
	// Verify BEFORE staging; a mismatch aborts without writing.
	return u.Stager.StageVerified(targetName, data, sums)
}

// findAsset returns the asset with the given name, if present.
func findAsset(r Release, name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}
