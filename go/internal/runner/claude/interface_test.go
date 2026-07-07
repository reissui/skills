package claude

import "github.com/reissui/clex/internal/core"

// Compile-time assertion that the adapter satisfies the shared Runner contract.
var _ core.Runner = (*Adapter)(nil)
