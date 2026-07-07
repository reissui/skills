package daemon

import (
	"testing"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/runner/fake"
)

func TestDefaultRunnerBuilderRegistersFakeProvider(t *testing.T) {
	r, err := DefaultRunnerBuilder("fake-model", config.Provider{
		Kind:   "fake",
		Binary: "/tmp/clex-fake-runner",
		Script: "/tmp/script.json",
	})
	if err != nil {
		t.Fatalf("DefaultRunnerBuilder(fake): %v", err)
	}
	if _, ok := r.(*fake.Adapter); !ok {
		t.Fatalf("runner = %T, want *fake.Adapter", r)
	}
}
