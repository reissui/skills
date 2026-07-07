package botflows

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update, when set (`go test ./internal/botflows -update`), rewrites the golden
// files under testdata/ from the current output instead of comparing. This is the
// standard Go golden-file idiom: the committed files ARE the tested contract, so
// any intended change to an outbound string is a reviewable diff in testdata/.
var update = flag.Bool("update", false, "update golden files")

// assertGolden compares got against the golden file testdata/<name>.golden,
// creating/updating it under -update. A mismatch fails with the file path so the
// exact string drift is obvious in review.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	want := string(wantBytes)
	if got != want {
		t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// joinLines renders a sequence of outbound messages as one golden blob, one
// message per line-group separated by a form-feed marker so multi-line messages
// (the plan gate) stay unambiguous. Single-line messages read as plain lines.
func joinLines(msgs []string) string {
	return strings.Join(msgs, "\n\x0c\n")
}
