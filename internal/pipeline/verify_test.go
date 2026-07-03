package pipeline

import (
	"context"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// TestVerificationTrust covers BOTH security paths of the verification-command
// trust policy (issue #15 acceptance criterion): a trusted author's body command
// is run verbatim; an untrusted author's body command is REPLACED by the repo
// default and the substitution is recorded.
func TestVerificationTrust(t *testing.T) {
	const (
		owner      = "reissui"
		self       = "clex-bot"
		bodyCmd    = "go test ./internal/secret/..."
		defaultCmd = "go test ./..."
	)
	p := New(Deps{}, Config{Owner: owner, SelfLogin: self, DefaultVerify: defaultCmd})

	tests := []struct {
		name        string
		author      string
		bodyVerify  string
		wantCommand string
		wantTrusted bool
		wantSubst   bool
	}{
		{
			name:        "owner-authored body command is trusted and run",
			author:      owner,
			bodyVerify:  bodyCmd,
			wantCommand: bodyCmd,
			wantTrusted: true,
			wantSubst:   false,
		},
		{
			name:        "clex-authored body command is trusted and run",
			author:      self,
			bodyVerify:  bodyCmd,
			wantCommand: bodyCmd,
			wantTrusted: true,
			wantSubst:   false,
		},
		{
			name:        "owner login differing only in case is still trusted",
			author:      "ReissUI",
			bodyVerify:  bodyCmd,
			wantCommand: bodyCmd,
			wantTrusted: true,
			wantSubst:   false,
		},
		{
			name:        "untrusted author falls back to repo default and records substitution",
			author:      "randostranger",
			bodyVerify:  bodyCmd,
			wantCommand: defaultCmd,
			wantTrusted: false,
			wantSubst:   true,
		},
		{
			name:        "empty author is untrusted",
			author:      "",
			bodyVerify:  bodyCmd,
			wantCommand: defaultCmd,
			wantTrusted: false,
			wantSubst:   true,
		},
		{
			name:        "trusted author with no body command uses repo default",
			author:      owner,
			bodyVerify:  "",
			wantCommand: defaultCmd,
			wantTrusted: true,
			wantSubst:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := &gh.Issue{
				Number:      42,
				AuthorLogin: tt.author,
				Meta:        gh.Metadata{Verify: tt.bodyVerify},
			}
			got := p.resolveVerification(iss)
			if got.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", got.Command, tt.wantCommand)
			}
			if got.Trusted != tt.wantTrusted {
				t.Errorf("Trusted = %v, want %v", got.Trusted, tt.wantTrusted)
			}
			if got.Substituted != tt.wantSubst {
				t.Errorf("Substituted = %v, want %v", got.Substituted, tt.wantSubst)
			}
			// Security invariant: an untrusted author's body command must NEVER
			// become the command that runs.
			if !tt.wantTrusted && got.Command == tt.bodyVerify && tt.bodyVerify != "" {
				t.Errorf("SECURITY: untrusted body command %q was selected to run", tt.bodyVerify)
			}
		})
	}
}

// TestBuildRunsTrustedVsSubstitutedCommand proves the BUILD STAGE actually
// executes the command the trust policy selected — the body command for a
// trusted author, the repo default for an untrusted one — by capturing what the
// verifier was asked to run in each case.
func TestBuildRunsTrustedVsSubstitutedCommand(t *testing.T) {
	const (
		owner      = "reissui"
		bodyCmd    = "make verify-me"
		defaultCmd = "go test ./..."
	)

	cases := []struct {
		name        string
		author      string
		wantRun     string
		wantSubNote bool
	}{
		{"trusted runs body command", owner, bodyCmd, false},
		{"untrusted runs repo default", "attacker", defaultCmd, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ghc := newFakeGH()
			ws := newFakeWS(t.TempDir())
			runner := &scriptedRunner{scripts: [][]core.Event{textThenResult("done")}}
			router := newFakeRouter()
			router.build = buildDecisionFor("qwen3-coder", "ollama")

			p := New(Deps{
				GH:      ghc,
				WS:      ws,
				Router:  router,
				Skills:  &fakeSkills{},
				Runners: newFakeFactory(runner),
			}, Config{Repo: testRepo(), RepoDir: t.TempDir(), Owner: owner, DefaultVerify: defaultCmd})

			var ranCmd string
			p.SetVerifierForTest(verifyFuncForTest(func(_ context.Context, dir, command string) error {
				ranCmd = command
				return nil
			}))

			issue := &gh.Issue{
				Number:      7,
				Title:       "do a thing",
				Body:        "acceptance criteria",
				AuthorLogin: tc.author,
				State:       core.StateBuilding,
				Meta:        gh.Metadata{Verify: bodyCmd, Difficulty: core.DifficultyStandard},
			}
			ghc.seedIssue(issue)

			res, err := p.Build(bg(), 1, issue, KnowledgeExcerpts{}, 0)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if ranCmd != tc.wantRun {
				t.Fatalf("verification ran %q, want %q", ranCmd, tc.wantRun)
			}
			if res.Verification.Substituted != tc.wantSubNote {
				t.Errorf("Substituted = %v, want %v", res.Verification.Substituted, tc.wantSubNote)
			}
		})
	}
}
