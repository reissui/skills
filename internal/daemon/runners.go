package daemon

import (
	"fmt"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/runner/claude"
	"github.com/reissui/clex/internal/runner/codex"
	"github.com/reissui/clex/internal/runner/fake"
	"github.com/reissui/clex/internal/runner/local"
)

// RunnerBuilder constructs a core.Runner for a given model id and its provider
// block. It exists so the daemon's production adapter wiring can be swapped for
// scripted fakes in integration tests. The returned runner must build its own
// allowlisted child environment (the adapters do this internally); the daemon
// never hands a runner the parent process env.
type RunnerBuilder func(modelID string, prov config.Provider) (core.Runner, error)

// DefaultRunnerBuilder maps a provider Kind to its official-CLI adapter. Each
// adapter shells out to claude / codex / codex --oss and applies its own
// minimal env allowlist (spec: Compliance note; Security model — least-
// privilege child processes). No adapter makes a direct provider API call.
func DefaultRunnerBuilder(modelID string, prov config.Provider) (core.Runner, error) {
	switch prov.Kind {
	case "claude-cli":
		return claude.New(), nil
	case "codex-cli":
		return codex.New(modelID), nil
	case "ollama":
		return local.New(modelID), nil
	case "fake":
		opts := []fake.Option{}
		if prov.Binary != "" {
			opts = append(opts, fake.WithBinary(prov.Binary))
		}
		if prov.Script != "" {
			opts = append(opts, fake.WithScript(prov.Script))
		}
		return fake.New(opts...), nil
	default:
		return nil, fmt.Errorf("daemon: unknown provider kind %q for model %q", prov.Kind, modelID)
	}
}

// runnerFactory implements pipeline.RunnerFactory (and supplies the registry's
// provider→runner map). It caches one runner per provider — an adapter is
// stateless and safe to reuse across issues, and codex/local adapters are keyed
// by model, so the cache key is "provider|model".
type runnerFactory struct {
	cfg     *config.Config
	build   RunnerBuilder
	byModel map[string]core.Runner // key: model id
}

// newRunnerFactory builds a factory over cfg using build to construct adapters.
// It eagerly instantiates one runner per configured model so both the registry
// (which wants a provider→runner map up front) and the pipeline (which asks per
// model) are served from the same instances.
func newRunnerFactory(cfg *config.Config, build RunnerBuilder) (*runnerFactory, error) {
	if build == nil {
		build = DefaultRunnerBuilder
	}
	f := &runnerFactory{cfg: cfg, build: build, byModel: make(map[string]core.Runner)}
	for id, m := range cfg.Models {
		prov, ok := cfg.Providers[m.Provider]
		if !ok {
			return nil, fmt.Errorf("daemon: model %q references undefined provider %q", id, m.Provider)
		}
		r, err := build(id, prov)
		if err != nil {
			return nil, err
		}
		f.byModel[id] = r
	}
	return f, nil
}

// RunnerFor returns the runner that executes model. It satisfies
// pipeline.RunnerFactory.
func (f *runnerFactory) RunnerFor(model core.Model) (pipeline.Runner, error) {
	if r, ok := f.byModel[model.ID]; ok {
		return r, nil
	}
	// Fall back to constructing on demand for a model not in the eager set
	// (e.g. a dynamically discovered local model that joined after startup).
	prov, ok := f.cfg.Providers[model.Provider]
	if !ok {
		return nil, fmt.Errorf("daemon: no provider %q for model %q", model.Provider, model.ID)
	}
	r, err := f.build(model.ID, prov)
	if err != nil {
		return nil, err
	}
	f.byModel[model.ID] = r
	return r, nil
}

// providerRunners returns a provider-name→runner map for registry.New. Each
// provider maps to one of its models' runners; claude uses a single shared
// adapter, while codex/local adapters are model-keyed but share env/permission
// behavior, so any one is a valid probe target for the provider's health.
func (f *runnerFactory) providerRunners() map[string]core.Runner {
	out := make(map[string]core.Runner)
	for id, m := range f.cfg.Models {
		if _, seen := out[m.Provider]; seen {
			continue
		}
		if r, ok := f.byModel[id]; ok {
			out[m.Provider] = r
		}
	}
	return out
}

// compile-time assertion that runnerFactory satisfies the pipeline contract.
var _ pipeline.RunnerFactory = (*runnerFactory)(nil)
