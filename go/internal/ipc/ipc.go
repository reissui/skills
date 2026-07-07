// Package ipc defines the local control protocol between the clexd daemon and
// the clex CLI. The daemon LISTENS on a Unix-domain socket under ~/.clex; the
// CLI (issue #17) dials it to drive status/pause/resume/stop/steer/models/costs
// without touching GitHub or Telegram directly.
//
// The protocol is deliberately tiny and self-describing: newline-delimited JSON
// frames, one Request per connection, one Response back, then close. This keeps
// the CLI a thin, dependency-free dialer and makes the wire format trivial to
// version. #17 imports THIS package as its only coupling point with the daemon;
// nothing else about the daemon's internals is exposed.
//
// Security (spec: Security model — "The IPC socket is 0600 under ~/.clex/"):
// Listen creates the socket file mode 0600 so only the owning OS user can
// connect. Callers are already trusted by filesystem permission; the protocol
// carries no authentication of its own.
package ipc

// ProtocolVersion is the wire-format version. Both ends send it; a mismatch is
// reported rather than guessed at, so an old CLI talking to a new daemon (or
// vice-versa) fails loud instead of misbehaving. Bump on any breaking change to
// the Request/Response shapes.
const ProtocolVersion = 1

// SocketName is the socket file's basename within the clex home directory.
// The full path is <home>/clexd.sock (see SocketPath).
const SocketName = "clexd.sock"

// Command names the daemon operation a Request invokes. Values are stable wire
// strings; do not renumber or repurpose them.
type Command string

const (
	// CmdStatus requests a snapshot of daemon and pipeline state.
	CmdStatus Command = "status"
	// CmdPause sets the global pause flag: no NEW dispatches, running work
	// continues (spec: Error handling & safety — global kill switch).
	CmdPause Command = "pause"
	// CmdResume clears the global pause flag.
	CmdResume Command = "resume"
	// CmdStop cancels the runner for a single issue (Request.Issue), reverts its
	// label to clex:approved, and PRESERVES its worktree.
	CmdStop Command = "stop"
	// CmdSteer injects steering text (Request.Text) toward an issue: as the next
	// turn of an active runner, or as a Steering note on an idle/epic issue.
	CmdSteer Command = "steer"
	// CmdModels reports the model registry health and routing.
	CmdModels Command = "models"
	// CmdCosts reports spend and estimates.
	CmdCosts Command = "costs"
)

// Request is a single control message from the CLI to the daemon. Exactly one
// is sent per connection, as one JSON object followed by a newline.
type Request struct {
	// Version is the sender's ProtocolVersion. The daemon rejects a mismatch.
	Version int `json:"version"`
	// Command is the operation to perform.
	Command Command `json:"command"`
	// Issue is the target issue number for CmdStop and CmdSteer. Zero for
	// commands that are not issue-scoped (and, for CmdSteer, means "the epic").
	Issue int `json:"issue,omitempty"`
	// Text is the steering text for CmdSteer. Ignored by other commands.
	Text string `json:"text,omitempty"`
}

// Response is the daemon's single reply to a Request, sent as one JSON object
// followed by a newline, after which the connection closes.
type Response struct {
	// Version is the daemon's ProtocolVersion, echoed so the CLI can detect a
	// skew even when OK is false.
	Version int `json:"version"`
	// OK reports whether the command succeeded. When false, Error explains why.
	OK bool `json:"ok"`
	// Error is a human-readable failure message; empty when OK is true.
	Error string `json:"error,omitempty"`
	// Message is a short human-readable success line (e.g. "paused",
	// "stopped #14"). Always safe to print.
	Message string `json:"message,omitempty"`
	// Status is populated for CmdStatus.
	Status *Status `json:"status,omitempty"`
	// Models is populated for CmdModels.
	Models []ModelHealth `json:"models,omitempty"`
	// Costs is populated for CmdCosts.
	Costs *Costs `json:"costs,omitempty"`
}

// Status is a snapshot of the daemon returned by CmdStatus. It is intentionally
// flat and display-oriented so the CLI can render it without further lookups.
type Status struct {
	// Version is the daemon build version.
	Version string `json:"version"`
	// Paused reports whether the global pause flag is set.
	Paused bool `json:"paused"`
	// Repo is the managed repository in "owner/name" form.
	Repo string `json:"repo"`
	// Running lists the issues with an active runner right now.
	Running []RunningIssue `json:"running,omitempty"`
	// PendingGate, when non-empty, describes a cost-gate confirmation the daemon
	// is currently waiting on (an epic held at GateConfirm).
	PendingGate string `json:"pending_gate,omitempty"`
	// Uptime is a human-readable daemon uptime string.
	Uptime string `json:"uptime,omitempty"`
	// Pipeline summarizes the count of issues in each pipeline state, keyed by
	// the bare state name (e.g. "planned", "building"). It powers `clex status`'s
	// pipeline view without the CLI re-reading GitHub. Additive in protocol v1;
	// an older daemon simply omits it.
	Pipeline map[string]int `json:"pipeline,omitempty"`
	// Explain carries the scheduler's human-readable reasons for its current
	// dispatch decisions (spec: scheduler Explain). `clex status` prints these so
	// the operator can see why work is or isn't running. Additive; may be nil.
	Explain []string `json:"explain,omitempty"`
}

// RunningIssue describes one in-flight runner in a Status snapshot.
type RunningIssue struct {
	Issue    int    `json:"issue"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Stage    string `json:"stage"`
}

// ModelHealth is one model's registry status returned by CmdModels.
type ModelHealth struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Healthy  bool   `json:"healthy"`
	Detail   string `json:"detail,omitempty"`
}

// Costs is the spend summary returned by CmdCosts.
type Costs struct {
	// SpentThisEpicUSD is the metered spend attributed to the active epic.
	SpentThisEpicUSD float64 `json:"spent_this_epic_usd"`
	// SpentTodayUSD is the metered spend since local midnight.
	SpentTodayUSD float64 `json:"spent_today_usd"`
	// Lines are human-readable per-model or per-stage cost lines.
	Lines []string `json:"lines,omitempty"`
	// EstimatedThisEpicUSD is the pre-run estimate for the active epic, so
	// `clex costs` can show estimate-vs-actual drift (spec: costs report drift).
	// Additive in protocol v1; zero when the daemon does not supply it.
	EstimatedThisEpicUSD float64 `json:"estimated_this_epic_usd,omitempty"`
}
