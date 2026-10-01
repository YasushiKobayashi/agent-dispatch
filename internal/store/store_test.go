package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	// A nested path also covers Open creating the parent directory.
	s, err := Open(filepath.Join(t.TempDir(), "nested", "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func event(cursor string) model.Event {
	return model.Event{
		Kind:       model.KindReviewRequested,
		Repo:       "YasushiKobayashi/agent-dispatch",
		Owner:      "YasushiKobayashi",
		Name:       "reviewd",
		PRNumber:   7,
		Cursor:     cursor,
		DetectedAt: time.Now(),
	}
}

func TestProcessedRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ev := event("abc123")

	if done, err := s.IsProcessed(ctx, ev); err != nil || done {
		t.Fatalf("IsProcessed before marking = %v, %v; want false, nil", done, err)
	}
	if err := s.MarkProcessed(ctx, ev); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if done, err := s.IsProcessed(ctx, ev); err != nil || !done {
		t.Fatalf("IsProcessed after marking = %v, %v; want true, nil", done, err)
	}

	// Marking twice must not fail: poll can race with a manual run.
	if err := s.MarkProcessed(ctx, ev); err != nil {
		t.Fatalf("MarkProcessed twice: %v", err)
	}
}

func TestProcessedIsScopedByCursor(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.MarkProcessed(ctx, event("sha-old")); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	// A new head SHA on the same PR is a new event and must run again.
	if done, err := s.IsProcessed(ctx, event("sha-new")); err != nil || done {
		t.Fatalf("IsProcessed(new cursor) = %v, %v; want false, nil", done, err)
	}
}

func TestRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ev := event("abc123")

	id, err := s.StartRun(ctx, ev, "review-requested", "/tmp/out")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	runs, err := s.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Status != model.RunRunning {
		t.Errorf("status = %q, want %q", runs[0].Status, model.RunRunning)
	}
	if runs[0].FinishedAt != nil {
		t.Error("FinishedAt set on a running run")
	}
	if runs[0].StartedAt.IsZero() {
		t.Error("StartedAt did not round-trip")
	}

	code := 0
	if err := s.FinishRun(ctx, id, model.RunSuccess, &code, "", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	runs, err = s.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	got := runs[0]
	if got.Status != model.RunSuccess {
		t.Errorf("status = %q, want %q", got.Status, model.RunSuccess)
	}
	if got.FinishedAt == nil {
		t.Fatal("FinishedAt is nil after FinishRun")
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", got.ExitCode)
	}
	if got.OutputDir != "/tmp/out" {
		t.Errorf("OutputDir = %q, want /tmp/out", got.OutputDir)
	}
}

func TestFinishRunWithoutExitCode(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	id, err := s.StartRun(ctx, event("c"), "r", "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(ctx, id, model.RunFailed, nil, "", "claude not found"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	runs, _ := s.RecentRuns(ctx, 1)
	if runs[0].ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", runs[0].ExitCode)
	}
	if runs[0].Error != "claude not found" {
		t.Errorf("Error = %q, want %q", runs[0].Error, "claude not found")
	}
}

func TestRecentRunsOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for i := 0; i < 5; i++ {
		if _, err := s.StartRun(ctx, event("c"), "rule", ""); err != nil {
			t.Fatalf("StartRun: %v", err)
		}
	}

	runs, err := s.RecentRuns(ctx, 3)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
	// Rows inserted in the same clock tick must still come back newest first.
	if runs[0].ID <= runs[1].ID || runs[1].ID <= runs[2].ID {
		t.Errorf("ids = %d, %d, %d; want descending", runs[0].ID, runs[1].ID, runs[2].ID)
	}
}

func TestRecentRunsEmpty(t *testing.T) {
	runs, err := newStore(t).RecentRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("got %d runs, want 0", len(runs))
	}
}
