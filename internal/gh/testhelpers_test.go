package gh

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newTestClient starts an httptest server using handler and returns a Client
// wired to it (base URL = server URL, no real token). The server is closed via
// t.Cleanup. This is the only way the test suite talks to "GitHub" — there are
// no live calls.
func newTestClient(t *testing.T, handler http.Handler, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	allOpts := append([]Option{WithBaseURL(srv.URL + "/")}, opts...)
	c, err := New("test-token", allOpts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// loadFixture reads a JSON fixture from testdata/.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}
