package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// VendoredSkill describes a third-party skill the installer fetches into the
// skills root. The set is fixed by the spec (Matt Pocock's planning skills);
// the coordinates mirror how setup-matt-pocock-skills sources them from the
// upstream mattpocock/skills repo (spec: Skills layer).
type VendoredSkill struct {
	Name    string // installed directory name, e.g. "to-prd"
	URL     string // source URL (the upstream repo)
	Version string // pinned version/ref (tag or commit)
	SubPath string // path to the skill's SKILL.md within the source repo
}

// vendoredSkills is the fixed fetch set (spec: Skills layer — to-prd,
// to-issues, grill-me, grill-with-docs, fetched the way
// setup-matt-pocock-skills does). Coordinates match the upstream layout
// recorded in the agent skill lockfile.
var vendoredSkills = []VendoredSkill{
	{Name: "to-prd", URL: mattPocockRepo, Version: mattPocockVersion, SubPath: "skills/engineering/to-prd/SKILL.md"},
	{Name: "to-issues", URL: mattPocockRepo, Version: mattPocockVersion, SubPath: "skills/engineering/to-issues/SKILL.md"},
	{Name: "grill-me", URL: mattPocockRepo, Version: mattPocockVersion, SubPath: "skills/productivity/grill-me/SKILL.md"},
	{Name: "grill-with-docs", URL: mattPocockRepo, Version: mattPocockVersion, SubPath: "skills/engineering/grill-with-docs/SKILL.md"},
}

const (
	// mattPocockRepo is the upstream source for the vendored planning skills.
	mattPocockRepo = "https://github.com/mattpocock/skills.git"
	// mattPocockVersion pins the ref the installer fetches. Bumping this must be
	// accompanied by re-pinning the sha256 in the lockfile.
	mattPocockVersion = "main"

	// lockFileName is the on-disk name of the supply-chain lockfile written into
	// the skills root.
	lockFileName = "skills.lock.json"
)

// Fetcher retrieves a vendored skill's SKILL.md bytes from its source. It is an
// interface so the network step is injectable: tests supply a stub and the
// "skip fetch" path passes nil. Implementations must be side-effect free beyond
// the network read (spec: Skills layer — the network step must be
// skippable/injectable).
type Fetcher interface {
	// Fetch returns the raw bytes of the skill's SKILL.md as published at the
	// given URL, version, and sub-path.
	Fetch(s VendoredSkill) ([]byte, error)
}

// LockEntry pins one vendored skill by URL, version, and sha256. A subsequent
// install whose fetched bytes hash differently fails, so upstream drift is
// caught (spec: Security model — supply chain: fetched skills pinned by URL +
// version + sha256; changed upstream content fails install until re-pinned).
type LockEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// Lockfile is the serialized supply-chain record written to the skills root.
type Lockfile struct {
	Skills []LockEntry `json:"skills"`
}

// InstallOptions configures InstallBundled.
type InstallOptions struct {
	// Fetcher performs the network reads for vendored skills. If nil, the fetch
	// step is skipped entirely: bundled skills are still installed and any
	// pre-existing lockfile is left untouched.
	Fetcher Fetcher
	// Expected pins the acceptable sha256 for each vendored skill by name. When
	// an entry is present, a fetched skill whose content hashes differently
	// fails the install. When empty/absent for a skill, the fetched hash is
	// recorded as the new pin (trust-on-first-use). Populate this from a
	// committed lockfile to enforce supply-chain integrity.
	Expected map[string]string
}

// InstallBundled materializes clex's skills into root (the user skills root,
// typically ~/.clex/skills): it copies the embedded clex-authored skills, and,
// when a Fetcher is provided, fetches the vendored third-party skills and
// writes a lockfile pinning each by URL, version, and sha256.
//
// The network step is fully optional: with opts.Fetcher == nil only the bundled
// skills are installed (no lockfile is written or modified). With a Fetcher, a
// fetched skill whose sha256 does not match opts.Expected[name] aborts the
// install with a clear error before anything is committed to disk (spec:
// Security model — hash mismatch fails install).
func InstallBundled(root string, opts InstallOptions) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("skills: create skills root %s: %w", root, err)
	}

	// 1. Install the embedded clex-authored skills.
	for _, name := range BundledNames() {
		if err := writeBundledSkill(name, filepath.Join(root, name)); err != nil {
			return err
		}
	}

	// 2. Optionally fetch the vendored skills and pin them. Fetch-and-verify all
	// of them before writing any, so a hash mismatch leaves the root in its
	// prior state for the vendored set.
	if opts.Fetcher == nil {
		return nil
	}

	type fetched struct {
		skill VendoredSkill
		data  []byte
		sum   string
	}
	got := make([]fetched, 0, len(vendoredSkills))
	for _, s := range vendoredSkills {
		data, err := opts.Fetcher.Fetch(s)
		if err != nil {
			return fmt.Errorf("skills: fetch %s from %s@%s: %w", s.Name, s.URL, s.Version, err)
		}
		sum := sha256Hex(data)
		if want, ok := opts.Expected[s.Name]; ok && want != "" && want != sum {
			return fmt.Errorf("skills: %w: %s@%s hash %s does not match pinned %s (upstream content changed; re-pin the lockfile to accept)",
				ErrHashMismatch, s.Name, s.Version, sum, want)
		}
		got = append(got, fetched{skill: s, data: data, sum: sum})
	}

	// 3. Commit fetched skills to disk and build the lockfile.
	lock := Lockfile{Skills: make([]LockEntry, 0, len(got))}
	for _, f := range got {
		dir := filepath.Join(root, f.skill.Name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("skills: create %s: %w", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, skillFileName), f.data, 0o600); err != nil {
			return fmt.Errorf("skills: write %s: %w", f.skill.Name, err)
		}
		lock.Skills = append(lock.Skills, LockEntry{
			Name:    f.skill.Name,
			URL:     f.skill.URL,
			Version: f.skill.Version,
			SHA256:  f.sum,
		})
	}
	sort.Slice(lock.Skills, func(i, j int) bool { return lock.Skills[i].Name < lock.Skills[j].Name })
	if err := writeLockfile(root, lock); err != nil {
		return err
	}
	return nil
}

// ErrHashMismatch is returned (wrapped) when a fetched skill's sha256 does not
// match its pinned value in the lockfile.
var ErrHashMismatch = errors.New("fetched skill hash mismatch")

// LoadLockfile reads and parses the lockfile from the skills root. A missing
// lockfile returns an empty Lockfile and no error.
func LoadLockfile(root string) (Lockfile, error) {
	data, err := os.ReadFile(filepath.Join(root, lockFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return Lockfile{}, nil
		}
		return Lockfile{}, fmt.Errorf("skills: read lockfile: %w", err)
	}
	var lf Lockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return Lockfile{}, fmt.Errorf("skills: parse lockfile: %w", err)
	}
	return lf, nil
}

// ExpectedFromLockfile projects a lockfile into the name→sha256 map that
// InstallBundled uses to enforce pins. Pass the result as InstallOptions.Expected
// to fail the install on any upstream drift.
func ExpectedFromLockfile(lf Lockfile) map[string]string {
	m := make(map[string]string, len(lf.Skills))
	for _, e := range lf.Skills {
		m[e.Name] = e.SHA256
	}
	return m
}

// writeLockfile serializes lf into root/skills.lock.json.
func writeLockfile(root string, lf Lockfile) error {
	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return fmt.Errorf("skills: marshal lockfile: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(root, lockFileName), data, 0o600); err != nil {
		return fmt.Errorf("skills: write lockfile: %w", err)
	}
	return nil
}

// sha256Hex returns the lowercase hex sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
