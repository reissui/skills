package pipeline

import (
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/workspace"
)

// Compile-time assertions that the concrete dependency types satisfy the narrow
// interfaces the pipeline declares. If a concrete package changes a signature
// these assertions fail to build, catching drift at compile time rather than at
// run time (issue #15: "compile-time var _ Iface = (*concrete.Type)(nil)
// assertions are good").
var (
	_ GitHub        = (*gh.Client)(nil)
	_ Workspace     = (*workspace.Manager)(nil)
	_ Router        = (*registry.Registry)(nil)
	_ SkillResolver = skillsAdapter{}
)
