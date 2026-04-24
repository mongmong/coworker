package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

func TestManager_StartSession(t *testing.T) {
	t.Parallel()

	db, runStore := setupManagerDependencies(t)
	defer db.Close()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".coworker", "session.lock")
	sm := &Manager{RunStore: runStore, LockPath: lockPath}

	runID, err := sm.StartSession()
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if runID == "" {
		t.Fatal("expected non-empty run ID")
	}

	got, err := sm.CurrentSession()
	if err != nil {
		t.Fatalf("CurrentSession() error after start = %v", err)
	}
	if got != runID {
		t.Errorf("CurrentSession() = %q, want %q", got, runID)
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[0] != runID {
		t.Fatalf("lock first line = %q, want run ID %q", lines[0], runID)
	}
	if len(lines) > 1 {
		if _, err := strconv.Atoi(lines[1]); err != nil {
			t.Fatalf("lock pid line invalid: %q", lines[1])
		}
	}
}

func TestManager_CurrentSession(t *testing.T) {
	t.Parallel()

	db, runStore := setupManagerDependencies(t)
	defer db.Close()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")
	sm := &Manager{RunStore: runStore, LockPath: lockPath}

	runID := "run_current_1"
	if err := runStore.CreateRun(context.Background(), &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%s\n%d", runID, os.Getpid())), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	got, err := sm.CurrentSession()
	if err != nil {
		t.Fatalf("CurrentSession() error = %v", err)
	}
	if got != runID {
		t.Fatalf("CurrentSession() = %q, want %q", got, runID)
	}
}

func TestManager_EndSession(t *testing.T) {
	t.Parallel()

	db, runStore := setupManagerDependencies(t)
	defer db.Close()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")
	sm := &Manager{RunStore: runStore, LockPath: lockPath}

	runID, err := sm.StartSession()
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if err := sm.EndSession(); err != nil {
		t.Fatalf("EndSession() error = %v", err)
	}

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock file to be removed, err = %v", err)
	}

	gotRun, err := runStore.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if gotRun.State != core.RunStateCompleted {
		t.Fatalf("run state = %q, want %q", gotRun.State, core.RunStateCompleted)
	}
}

func TestManager_CurrentSession_NoActiveSession(t *testing.T) {
	db, runStore := setupManagerDependencies(t)
	defer db.Close()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")
	sm := &Manager{RunStore: runStore, LockPath: lockPath}

	_, err := sm.CurrentSession()
	if err == nil {
		t.Fatal("expected error when no lock exists")
	}
	if !errors.Is(err, ErrNoActiveSession) {
		t.Fatalf("CurrentSession() error = %v, want %v", err, ErrNoActiveSession)
	}
}

func TestManager_CurrentSession_StaleLock(t *testing.T) {
	db, runStore := setupManagerDependencies(t)
	defer db.Close()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")
	sm := &Manager{RunStore: runStore, LockPath: lockPath}

	staleRunID := "run_stale"
	if err := runStore.CreateRun(context.Background(), &core.Run{
		ID:        staleRunID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create stale run: %v", err)
	}
	if err := runStore.CompleteRun(context.Background(), staleRunID, core.RunStateCompleted); err != nil {
		t.Fatalf("complete stale run: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%s\n%d", staleRunID, 99999)), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	_, err := sm.CurrentSession()
	if err == nil {
		t.Fatal("expected stale lock to be treated as no active session")
	}
	if !errors.Is(err, ErrNoActiveSession) {
		t.Fatalf("CurrentSession() error = %v, want %v", err, ErrNoActiveSession)
	}
}

func setupManagerDependencies(t *testing.T) (*store.DB, *store.RunStore) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	eventStore := store.NewEventStore(db)
	return db, store.NewRunStore(db, eventStore)
}
