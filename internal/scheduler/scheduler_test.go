package scheduler

import (
	"testing"
	"time"
	"webhooktimer/internal/model"
	"webhooktimer/internal/store"
)

func TestNextExecutionFromShiftsOutsideSleepWindow(t *testing.T) {
	loc := time.UTC
	m := New(store.New(t.TempDir()+"/state.json"), loc)

	entry := model.Entry{
		Mode:         model.ModeFixed,
		FixedSeconds: 120,
		SleepEnabled: true,
		SleepStart:   "23:00",
		SleepEnd:     "06:00",
	}

	now := time.Date(2026, 1, 1, 22, 59, 30, 0, loc)
	next := m.nextExecutionFrom(entry, now)

	want := time.Date(2026, 1, 2, 6, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next execution mismatch: got %s want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestNextExecutionFromWhileAlreadySleeping(t *testing.T) {
	loc := time.UTC
	m := New(store.New(t.TempDir()+"/state.json"), loc)

	entry := model.Entry{
		Mode:         model.ModeFixed,
		FixedSeconds: 30,
		SleepEnabled: true,
		SleepStart:   "23:00",
		SleepEnd:     "06:00",
	}

	now := time.Date(2026, 1, 1, 1, 0, 0, 0, loc)
	next := m.nextExecutionFrom(entry, now)

	want := time.Date(2026, 1, 1, 6, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next execution mismatch: got %s want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestIsSleepModeActiveBoundary(t *testing.T) {
	loc := time.UTC
	m := New(store.New(t.TempDir()+"/state.json"), loc)

	entry := model.Entry{
		SleepEnabled: true,
		SleepStart:   "23:00",
		SleepEnd:     "06:00",
	}

	start := time.Date(2026, 1, 1, 23, 0, 0, 0, loc)
	if !m.isSleepModeActive(entry, start) {
		t.Fatal("expected sleep window to be active at the configured start")
	}

	end := time.Date(2026, 1, 2, 6, 0, 0, 0, loc)
	if m.isSleepModeActive(entry, end) {
		t.Fatal("expected sleep window to be inactive at the configured end")
	}
}
