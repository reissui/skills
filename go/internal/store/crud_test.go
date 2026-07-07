package store

import (
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

func TestSessionRoundTrip(t *testing.T) {
	st, _ := openTemp(t)

	id, err := st.CreateSession(Session{
		Issue:     42,
		Repo:      "owner/repo",
		Model:     "opus-4-8",
		State:     SessionRunning,
		StartedAt: ts,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := st.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Issue != 42 || got.Repo != "owner/repo" || got.Model != "opus-4-8" {
		t.Fatalf("session fields round-tripped wrong: %+v", got)
	}
	if got.State != SessionRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
	if !got.StartedAt.Equal(ts) {
		t.Fatalf("started_at = %v, want %v", got.StartedAt, ts)
	}
	if !got.EndedAt.IsZero() {
		t.Fatalf("ended_at should be zero while running, got %v", got.EndedAt)
	}

	// Update the resume id.
	if err := st.SetSessionCLIID(id, "cli-abc"); err != nil {
		t.Fatalf("SetSessionCLIID: %v", err)
	}

	// RunningSessions should include it before finishing.
	running, err := st.RunningSessions()
	if err != nil {
		t.Fatalf("RunningSessions: %v", err)
	}
	if len(running) != 1 || running[0].ID != id || running[0].CLISession != "cli-abc" {
		t.Fatalf("RunningSessions = %+v, want the one running session with cli id", running)
	}

	// Finish it; it should leave the running set and carry ended_at.
	end := ts.Add(90 * time.Second)
	if err := st.FinishSession(id, SessionDone, end); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	running, err = st.RunningSessions()
	if err != nil {
		t.Fatalf("RunningSessions after finish: %v", err)
	}
	if len(running) != 0 {
		t.Fatalf("RunningSessions after finish = %d, want 0", len(running))
	}

	byIssue, err := st.SessionsForIssue(42)
	if err != nil {
		t.Fatalf("SessionsForIssue: %v", err)
	}
	if len(byIssue) != 1 || byIssue[0].State != SessionDone || !byIssue[0].EndedAt.Equal(end) {
		t.Fatalf("SessionsForIssue = %+v, want one done session ended at %v", byIssue, end)
	}
}

func TestUsageRoundTrip(t *testing.T) {
	st, _ := openTemp(t)

	rec := UsageRecord{
		Model:      "sonnet-5",
		Stage:      string(core.RoleBuild),
		Difficulty: core.DifficultyStandard,
		Tokens:     core.Usage{In: 1200, Out: 3400},
		CostUSD:    0.42,
		Duration:   75 * time.Second,
		Success:    true,
		TS:         ts,
	}
	if _, err := st.RecordUsage(rec); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	got, err := st.UsageForModel("sonnet-5")
	if err != nil {
		t.Fatalf("UsageForModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("UsageForModel len = %d, want 1", len(got))
	}
	u := got[0]
	if u.Stage != string(core.RoleBuild) || u.Difficulty != core.DifficultyStandard {
		t.Fatalf("stage/difficulty round-tripped wrong: %+v", u)
	}
	if u.Tokens.In != 1200 || u.Tokens.Out != 3400 {
		t.Fatalf("tokens = %+v, want In=1200 Out=3400", u.Tokens)
	}
	if u.CostUSD != 0.42 {
		t.Fatalf("cost = %v, want 0.42", u.CostUSD)
	}
	if u.Duration != 75*time.Second {
		t.Fatalf("duration = %v, want 75s", u.Duration)
	}
	if !u.Success {
		t.Fatalf("success = false, want true")
	}
	if !u.TS.Equal(ts) {
		t.Fatalf("ts = %v, want %v", u.TS, ts)
	}
}

func TestEstimateRoundTrip(t *testing.T) {
	st, _ := openTemp(t)

	id, err := st.RecordEstimate(Estimate{
		Issue:        7,
		Stage:        "plan",
		Model:        "fable-5",
		EstimatedUSD: 6.20,
		TS:           ts,
	})
	if err != nil {
		t.Fatalf("RecordEstimate: %v", err)
	}

	// Record the actual once the stage completes.
	if err := st.RecordActual(id, 5.05); err != nil {
		t.Fatalf("RecordActual: %v", err)
	}

	got, err := st.EstimatesForIssue(7)
	if err != nil {
		t.Fatalf("EstimatesForIssue: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("EstimatesForIssue len = %d, want 1", len(got))
	}
	e := got[0]
	if e.Model != "fable-5" || e.EstimatedUSD != 6.20 || e.ActualUSD != 5.05 {
		t.Fatalf("estimate round-tripped wrong: %+v", e)
	}
}

func TestTelegramMapRoundTrip(t *testing.T) {
	st, _ := openTemp(t)

	if err := st.PutTelegramMap(TelegramLink{MsgID: 555, Issue: 42, IsEpic: false}); err != nil {
		t.Fatalf("PutTelegramMap: %v", err)
	}

	got, err := st.TelegramByMsg(555)
	if err != nil {
		t.Fatalf("TelegramByMsg: %v", err)
	}
	if got.Issue != 42 || got.IsEpic {
		t.Fatalf("telegram link round-tripped wrong: %+v", got)
	}

	msg, err := st.TelegramMsgForIssue(42)
	if err != nil {
		t.Fatalf("TelegramMsgForIssue: %v", err)
	}
	if msg != 555 {
		t.Fatalf("TelegramMsgForIssue = %d, want 555", msg)
	}

	// Upsert: relinking the same msg id overwrites the target.
	if err := st.PutTelegramMap(TelegramLink{MsgID: 555, Issue: 99, IsEpic: true}); err != nil {
		t.Fatalf("PutTelegramMap upsert: %v", err)
	}
	got, err = st.TelegramByMsg(555)
	if err != nil {
		t.Fatalf("TelegramByMsg after upsert: %v", err)
	}
	if got.Issue != 99 || !got.IsEpic {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	if err := st.DeleteTelegramMap(555); err != nil {
		t.Fatalf("DeleteTelegramMap: %v", err)
	}
	if _, err := st.TelegramByMsg(555); err != ErrNotFound {
		t.Fatalf("after delete err = %v, want ErrNotFound", err)
	}
}

func TestImageQueueRoundTrip(t *testing.T) {
	st, _ := openTemp(t)

	id, err := st.EnqueueImage(QueuedImage{
		Path:       "/spool/clex/img-1.png",
		Issue:      42,
		ReceivedAt: ts,
	})
	if err != nil {
		t.Fatalf("EnqueueImage: %v", err)
	}

	pending, err := st.PendingImages(42)
	if err != nil {
		t.Fatalf("PendingImages: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id || pending[0].Path != "/spool/clex/img-1.png" {
		t.Fatalf("PendingImages = %+v, want one pending image", pending)
	}
	if pending[0].Consumed {
		t.Fatalf("newly enqueued image should not be consumed")
	}

	if err := st.ConsumeImage(id); err != nil {
		t.Fatalf("ConsumeImage: %v", err)
	}
	pending, err = st.PendingImages(42)
	if err != nil {
		t.Fatalf("PendingImages after consume: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("PendingImages after consume = %d, want 0", len(pending))
	}
}

func TestEventLogRoundTrip(t *testing.T) {
	st, _ := openTemp(t)

	if _, err := st.AppendEvent(LogEntry{TS: ts, Issue: 42, Kind: "dispatch", Detail: "build on sonnet-5"}); err != nil {
		t.Fatalf("AppendEvent issue-scoped: %v", err)
	}
	if _, err := st.AppendEvent(LogEntry{TS: ts.Add(time.Second), Issue: 0, Kind: "boot", Detail: "daemon up"}); err != nil {
		t.Fatalf("AppendEvent global: %v", err)
	}

	forIssue, err := st.EventsForIssue(42)
	if err != nil {
		t.Fatalf("EventsForIssue: %v", err)
	}
	if len(forIssue) != 1 || forIssue[0].Kind != "dispatch" || forIssue[0].Detail != "build on sonnet-5" {
		t.Fatalf("EventsForIssue = %+v, want the one dispatch event", forIssue)
	}
	if !forIssue[0].TS.Equal(ts) {
		t.Fatalf("event ts = %v, want %v", forIssue[0].TS, ts)
	}

	recent, err := st.RecentEvents(10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("RecentEvents = %d, want 2", len(recent))
	}
	// Newest first: the "boot" event has the later timestamp.
	if recent[0].Kind != "boot" {
		t.Fatalf("RecentEvents not newest-first: got %q first", recent[0].Kind)
	}
}
