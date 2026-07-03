package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// bundledFS holds the clex-authored skills that ship in the binary. The
// authoritative human-facing copies live at the repo root under skills/; the
// copies here are kept byte-identical (guarded by a test) so the compiled
// binary can install them with no repo checkout. Each entry is a skill
// directory containing a SKILL.md.
//
//go:embed all:bundled
var bundledFS embed.FS

// bundledRoot is the path prefix inside bundledFS.
const bundledRoot = "bundled"

// bundledVirtualPrefix marks a SkillDir.Path as referring to an embedded skill
// rather than an on-disk directory. It is not a real filesystem path.
const bundledVirtualPrefix = "clex-bundled://"

// BundledFS returns the embedded filesystem of clex-authored skills, rooted so
// that each top-level entry is a skill directory (e.g. "clex-plan/SKILL.md").
// Exposed for callers and tests that need to read bundled content directly.
func BundledFS() (fs.FS, error) {
	return fs.Sub(bundledFS, bundledRoot)
}

// BundledNames returns the sorted names of the skills embedded in the binary.
func BundledNames() []string {
	sub, err := fs.Sub(bundledFS, bundledRoot)
	if err != nil {
		return nil
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// bundledHas reports whether name is a bundled skill directory.
func bundledHas(name string) bool {
	info, err := fs.Stat(bundledFS, bundledRoot+"/"+name+"/"+skillFileName)
	return err == nil && !info.IsDir()
}

// bundledVirtualPath returns the sentinel Path used in a SkillDir for a bundled
// skill.
func bundledVirtualPath(name string) string {
	return bundledVirtualPrefix + name
}

// isBundledPath reports whether p is a bundled-skill sentinel path.
func isBundledPath(p string) bool {
	return strings.HasPrefix(p, bundledVirtualPrefix)
}

// writeBundledSkill copies the embedded skill named name into the directory
// dest (which is created). Used both by SymlinkInto (bundled skills cannot be
// symlinked) and by InstallBundled.
func writeBundledSkill(name, dest string) error {
	sub, err := fs.Sub(bundledFS, bundledRoot+"/"+name)
	if err != nil {
		return fmt.Errorf("skills: locate bundled %q: %w", name, err)
	}
	return copyFSDir(sub, dest)
}

// copyFSDir copies every regular file in src (an fs.FS) into the on-disk
// directory dest, preserving relative structure. Directories are created 0700
// and files written 0600.
func copyFSDir(src fs.FS, dest string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, p)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return fmt.Errorf("skills: read bundled %s: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return fmt.Errorf("skills: write %s: %w", target, err)
		}
		return nil
	})
}
