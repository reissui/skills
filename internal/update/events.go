package update

// This file defines the events internal/update PRODUCES. They are consumed by
// the Telegram/CLI surfaces (#17/#18) — this package never renders them and
// never blocks on a human. An update run collects zero or more of these; the
// caller decides how to surface each one (a one-tap Telegram confirm, a y/N in
// `clex update`, or a silent log). Keeping them as plain data with no behaviour
// keeps the update engine head-less and trivially testable (spec: Self-update).

// ProposalKind classifies a proposal so the surface can pick a rendering.
type ProposalKind string

const (
	// KindReleaseConfirm is a clex-binary upgrade that is larger than a patch
	// (or any upgrade when update.auto != "patch"): it needs an explicit owner
	// confirm before staging. Rendered by #17/#18 as
	// `clex 0.4.0 available — update? [✓ yes] [changelog] [skip]`.
	KindReleaseConfirm ProposalKind = "release_confirm"
	// KindModelAdd proposes adding a newly discovered model to a tier, e.g.
	// `sonnet-5.1 detected — add to mid? [✓] [ignore]` (spec: Self-update
	// layer 3).
	KindModelAdd ProposalKind = "model_add"
	// KindModelRemove proposes a config fix-up for a configured model that a
	// re-probe no longer reports (retired upstream).
	KindModelRemove ProposalKind = "model_remove"
	// KindModelRename proposes a config fix-up when a configured model appears
	// to have been renamed upstream (an old id vanished and a near-identical new
	// id appeared under the same provider).
	KindModelRename ProposalKind = "model_rename"
)

// Proposal is a single actionable suggestion the update engine surfaces to the
// owner. Fields are populated per Kind; irrelevant fields stay zero. It carries
// no callbacks — acting on it is the surface's job.
type Proposal struct {
	// Kind selects which fields are meaningful and how to render.
	Kind ProposalKind

	// Message is a ready-to-send one-line summary (no keyboard), suitable for a
	// Telegram line or a CLI prompt. Surfaces may reformat, but this is a safe
	// default that never leaks secrets.
	Message string

	// Release is set for KindReleaseConfirm: the newer version offered and its
	// bump class relative to the running binary.
	Release *ReleaseProposal

	// Model is set for the model-* kinds: which model, provider, and (for adds)
	// the guessed tier or (for renames) the old→new id pair.
	Model *ModelProposal
}

// ReleaseProposal describes an offered clex release that requires confirmation.
type ReleaseProposal struct {
	// Current is the running binary's version string (as reported by the caller).
	Current string
	// Latest is the newer release tag (normalized, e.g. "0.4.0").
	Latest string
	// Bump is the upgrade class (BumpMinor or BumpMajor for a confirm; a
	// BumpPatch is auto-staged and never produces a confirm under auto="patch").
	Bump Bump
}

// ModelProposal describes a model tier proposal or config fix-up.
type ModelProposal struct {
	// Provider is the provider block the model belongs to.
	Provider string
	// Model is the model id in question (for a rename this is the NEW id).
	Model string
	// Tier is the suggested tier for KindModelAdd (a sensible guess); empty for
	// remove/rename.
	Tier string
	// OldModel is set only for KindModelRename: the configured id that appears to
	// have been renamed to Model.
	OldModel string
}
