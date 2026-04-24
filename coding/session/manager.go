package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

const defaultSessionLockPath = ".coworker/session.lock"

var (
	ErrNoActiveSession  = errors.New("no active session")
	errInvalidSession   = errors.New("invalid session lock")
	errMissingRunStore  = errors.New("run store is required")
)

// SessionManager manages interactive session lifecycle + lock file state.
type SessionManager struct {
	RunStore *store.RunStore
	LockPath string
	PID      int
}

// StartSession creates a run and stores the active session ID in the lock file.
func (sm *SessionManager) StartSession() (string, error) {
	if sm.RunStore == nil {
		return "", errMissingRunStore
	}

	runID := core.NewID()
	run := &core.Run{
		ID:        runID,
		Mode:      "interactive",
		State:     core.RunStateActive,
		StartedAt: time.Now(),
	}

	if err := sm.RunStore.CreateRun(context.Background(), run); err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if err := sm.writeLock(runID); err != nil {
		_ = sm.RunStore.CompleteRun(context.Background(), runID, core.RunStateFailed)
		return "", fmt.Errorf("write session lock: %w", err)
	}

	return runID, nil
}

// CurrentSession returns the active run ID from the lock file.
//
// A session is considered inactive if there is no lock file, the lock file
// is malformed, the run cannot be loaded, or the run state is not active.
func (sm *SessionManager) CurrentSession() (string, error) {
	if sm.RunStore == nil {
		return "", errMissingRunStore
	}

	lockRunID, err := sm.readLock()
	if err != nil {
		return "", err
	}

	run, err := sm.RunStore.GetRun(context.Background(), lockRunID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoActiveSession
		}
		return "", fmt.Errorf("get run %q: %w", lockRunID, err)
	}

	if run == nil || run.State != core.RunStateActive {
		return "", ErrNoActiveSession
	}

	return lockRunID, nil
}

// EndSession completes the active run and removes the lock file.
func (sm *SessionManager) EndSession() error {
	if sm.RunStore == nil {
		return errMissingRunStore
	}

	runID, err := sm.CurrentSession()
	if err != nil {
		return err
	}

	if err := sm.RunStore.CompleteRun(context.Background(), runID, core.RunStateCompleted); err != nil {
		return fmt.Errorf("complete run %q: %w", runID, err)
	}

	if err := os.Remove(sm.lockPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove session lock: %w", err)
	}

	return nil
}

func (sm *SessionManager) writeLock(runID string) error {
	lockPath := sm.lockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}

	lines := []string{runID}
	pid := sm.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	if pid > 0 {
		lines = append(lines, strconv.Itoa(pid))
	}

	payload := strings.Join(lines, "\n")
	tmp := lockPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("write temporary lock file: %w", err)
	}
	if err := os.Rename(tmp, lockPath); err != nil {
		return fmt.Errorf("install lock file: %w", err)
	}

	return nil
}

func (sm *SessionManager) readLock() (string, error) {
	data, err := os.ReadFile(sm.lockPath())
	if os.IsNotExist(err) {
		return "", ErrNoActiveSession
	}
	if err != nil {
		return "", fmt.Errorf("read session lock: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", fmt.Errorf("%w: missing run ID", errInvalidSession)
	}

	if len(lines) >= 2 {
		pidLine := strings.TrimSpace(lines[1])
		if pidLine != "" {
			if _, err := strconv.Atoi(pidLine); err != nil {
				return "", fmt.Errorf("%w: invalid PID", errInvalidSession)
			}
		}
	}

	return strings.TrimSpace(lines[0]), nil
}

func (sm *SessionManager) lockPath() string {
	if sm.LockPath == "" {
		return defaultSessionLockPath
	}
	return sm.LockPath
}
