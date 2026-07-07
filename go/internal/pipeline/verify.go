package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/reissui/clex/internal/gh"
)

// VerificationPlan is the resolved decision about which verification command a
// build will run, and why. It is a typed value so the build stage can record the
// substitution and surface it (spec: Security model — "The command is always
// visible at the plan gate").
type VerificationPlan struct {
	// Command is the command line that will actually run.
	Command string
	// Trusted reports whether the issue body was owner-/clex-authored, meaning
	// the body's own Verify command is honored.
	Trusted bool
	// Substituted is true when the body's command was NOT honored and the repo
	// default was used instead (untrusted author, or trusted author with no
	// command). It drives the "notes the substitution" requirement.
	Substituted bool
	// BodyCommand is the command declared in the issue body (may be empty).
	BodyCommand string
	// Reason is a short human-readable explanation for logs/comments.
	Reason string
}

// resolveVerification decides which verification command the build stage runs
// for iss, per the security policy: the issue body's Verify command is honored
// ONLY when the issue was authored/last-edited by the owner or clex; otherwise
// the repo's configured default command is used and the substitution is noted
// (spec: Security model — "Verification commands ... are only honored from issue
// bodies authored (and last edited) by the owner or clex; anything else falls
// back to the repo's configured default verification command").
//
// This is a pure function of the issue and config so both paths are trivially
// testable without any runner or filesystem.
func (p *Pipeline) resolveVerification(iss *gh.Issue) VerificationPlan {
	body := strings.TrimSpace(iss.Meta.Verify)
	trusted := p.trustsAuthor(iss.AuthorLogin)

	switch {
	case trusted && body != "":
		return VerificationPlan{
			Command:     body,
			Trusted:     true,
			Substituted: false,
			BodyCommand: body,
			Reason:      fmt.Sprintf("verification command trusted (author %q)", iss.AuthorLogin),
		}
	case trusted && body == "":
		// Trusted author but declared no command: fall back to the repo default.
		// Not a security substitution, but still a substitution of source.
		return VerificationPlan{
			Command:     p.cfg.DefaultVerify,
			Trusted:     true,
			Substituted: true,
			BodyCommand: "",
			Reason:      "issue declares no verification command; using repo default",
		}
	default:
		// Untrusted author: never honor the body command.
		return VerificationPlan{
			Command:     p.cfg.DefaultVerify,
			Trusted:     false,
			Substituted: true,
			BodyCommand: body,
			Reason: fmt.Sprintf(
				"verification command NOT trusted (author %q is not owner/clex); using repo default",
				iss.AuthorLogin),
		}
	}
}

// trustsAuthor reports whether login is the configured owner or clex's own
// account. Comparison is case-insensitive because GitHub logins are.
func (p *Pipeline) trustsAuthor(login string) bool {
	if login == "" {
		return false
	}
	l := strings.ToLower(login)
	return (p.cfg.Owner != "" && l == strings.ToLower(p.cfg.Owner)) ||
		(p.cfg.SelfLogin != "" && l == strings.ToLower(p.cfg.SelfLogin))
}

// verifyRunner runs a resolved verification command in a worktree and reports
// success. It is an interface so the build stage's command execution can be
// faked in tests (no real shell-out), while production uses shellVerifier.
type verifyRunner interface {
	run(ctx context.Context, dir, command string) error
}

// shellVerifier executes the command via "sh -c" in dir. The child receives the
// process environment as-is here because the build stage is responsible for
// constructing the allowlisted environment for model runners; verification is a
// deterministic owner-authored (or repo-default) command, run by clex itself.
// The runner CLIs — not this command — are the untrusted-code surface.
type shellVerifier struct{}

func (shellVerifier) run(ctx context.Context, dir, command string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("%w: empty verification command", ErrVerificationFailed)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %v\n%s", ErrVerificationFailed, err, string(out))
	}
	return nil
}

// wrapVerifyErr normalizes an arbitrary verifier error to ErrVerificationFailed
// while preserving detail.
func wrapVerifyErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrVerificationFailed) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrVerificationFailed, err)
}
