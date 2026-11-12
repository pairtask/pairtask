package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func TestAgentRolesAcceptancePersistUpdateAndClearAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.db")
	db, err := store.Open(path)
	pairOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)
	first, err := db.SetAgentRole(ctx, "codex-1", "CI tests and reviews", at)
	pairOK(t, err)
	pairEqual(t, first.AgentID, "codex-1")
	pairEqual(t, first.Role, "CI tests and reviews")
	pairEqual(t, first.UpdatedAt.Equal(at), true)
	_, err = db.SetAgentRole(ctx, "claude-1", "implementation and docs", at)
	pairOK(t, err)
	updated, err := db.SetAgentRole(ctx, "codex-1", "regression tests and merge gates", at.Add(time.Minute))
	pairOK(t, err)
	pairEqual(t, updated.UpdatedAt.After(first.UpdatedAt), true)
	pairOK(t, db.Close())
	db, err = store.Open(path)
	pairOK(t, err)
	roles, err := db.AgentRoles(ctx)
	pairOK(t, err)
	pairEqual(t, len(roles), 2)
	pairEqual(t, roles["codex-1"], "regression tests and merge gates")
	pairEqual(t, roles["claude-1"], "implementation and docs")
	cleared, err := db.SetAgentRole(ctx, "codex-1", "", at.Add(2*time.Minute))
	pairOK(t, err)
	pairEqual(t, cleared.Role, "")
	roles, err = db.AgentRoles(ctx)
	pairOK(t, err)
	if _, ok := roles["codex-1"]; ok {
		t.Fatal("cleared role still reaches Jev's roster")
	}
	pairEqual(t, roles["claude-1"], "implementation and docs")
}

func TestAgentRolesAcceptancePredictionWindowUsesExactEligibleIDs(t *testing.T) {
	db := pairSQLite(t, filepath.Join(t.TempDir(), "feedback.db"))
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)
	for i, item := range []struct {
		id       string
		eligible []string
		outcome  string
	}{
		{"old-own", []string{"agent%", "other"}, store.PredictionAbstain},
		{"other-only", []string{"agent", "other"}, store.PredictionAbstain},
		{"new-own", []string{"agent%", "other"}, store.PredictionAbstain},
		{"newer-skipped", []string{"agent%", "other"}, store.PredictionSkipped},
		{"newest-error", []string{"agent%", "other"}, store.PredictionError},
	} {
		_, err := db.RecordPrediction(ctx, store.Prediction{MessageID: item.id, Outcome: item.outcome, Eligible: item.eligible, CreatedAt: at.Add(time.Duration(i) * time.Second)})
		pairOK(t, err)
	}
	rows, err := db.PredictionsFor(ctx, "agent%", 1)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].MessageID, "new-own")
	rows, err = db.PredictionsFor(ctx, "agent%", 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 2)
	pairEqual(t, rows[0].MessageID, "new-own")
	pairEqual(t, rows[1].MessageID, "old-own")
}
