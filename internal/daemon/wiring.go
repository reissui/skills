package daemon

import (
	"log/slog"
	"path/filepath"

	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/workspace"
)

// workspaceManager builds the production workspace.Manager for the pipeline. It
// is rooted at the clex home so worktrees live under <home>/worktrees.
func workspaceManager(home string, log *slog.Logger) pipeline.Workspace {
	return workspace.New(home, log)
}

// repoDirFor resolves the primary on-disk checkout that owns worktrees for the
// managed repo. Convention: <home>/repos/<name>. The build/assemble stages
// create worktrees and the integration branch beneath it.
func repoDirFor(cfg Config) string {
	return filepath.Join(cfg.Home, "repos", cfg.Repo.Name)
}
