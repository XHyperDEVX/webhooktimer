package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"webhooktimer/internal/model"
)

var ErrNotFound = errors.New("entry not found")

type Store struct {
	mu      sync.RWMutex
	path    string
	entries map[string]model.Entry
	logs    map[string][]model.LogEntry
}

type persistedState struct {
	Entries []model.Entry               `json:"entries"`
	Logs    map[string][]model.LogEntry `json:"logs"`
}

func New(path string) *Store {
	return &Store{
		path:    path,
		entries: make(map[string]model.Entry),
		logs:    make(map[string][]model.LogEntry),
	}
}

func (s *Store) Load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read state file: %w", err)
	}

	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().UTC().Unix())
		_ = os.Rename(s.path, backup)
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = make(map[string]model.Entry, len(state.Entries))
	for _, entry := range state.Entries {
		s.entries[entry.ID] = entry
	}

	if state.Logs == nil {
		s.logs = make(map[string][]model.LogEntry)
	} else {
		s.logs = make(map[string][]model.LogEntry, len(state.Logs))
		for id, logs := range state.Logs {
			s.logs[id] = append([]model.LogEntry(nil), logs...)
		}
	}

	return nil
}

func (s *Store) ListEntries() []model.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]model.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries
}

func (s *Store) APIEntries(logLimit int) []model.APIEntry {
	entries := s.ListEntries()
	result := make([]model.APIEntry, 0, len(entries))

	for _, entry := range entries {
		result = append(result, model.ToAPIEntry(entry, s.Logs(entry.ID, logLimit)))
	}

	return result
}

func (s *Store) GetEntry(id string) (model.Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.entries[id]
	return entry, ok
}

func (s *Store) UpsertEntry(entry model.Entry) error {
	now := time.Now().UTC()

	s.mu.Lock()
	if existing, ok := s.entries[entry.ID]; ok {
		entry.CreatedAt = existing.CreatedAt
		entry.LastExecution = existing.LastExecution
		entry.LastResult = existing.LastResult
		entry.NextExecutionAt = existing.NextExecutionAt
	} else if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	s.entries[entry.ID] = entry
	snapshot := s.snapshotLocked()
	s.mu.Unlock()

	return s.persist(snapshot)
}

func (s *Store) DeleteEntry(id string) error {
	s.mu.Lock()
	if _, ok := s.entries[id]; !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.entries, id)
	delete(s.logs, id)
	snapshot := s.snapshotLocked()
	s.mu.Unlock()

	return s.persist(snapshot)
}

func (s *Store) SetActive(id string, active bool) error {
	now := time.Now().UTC()

	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	entry.Active = active
	entry.UpdatedAt = now
	if !active {
		entry.NextExecutionAt = time.Time{}
	}
	s.entries[id] = entry
	snapshot := s.snapshotLocked()
	s.mu.Unlock()

	return s.persist(snapshot)
}

func (s *Store) SetNextExecution(id string, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	entry.NextExecutionAt = next
	s.entries[id] = entry
	return nil
}

func (s *Store) RecordExecution(id string, result model.ExecuteResult) error {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}

	entry.LastExecution = result.Timestamp
	if result.Success {
		entry.LastResult = fmt.Sprintf("success (%d)", result.StatusCode)
	} else {
		entry.LastResult = result.Message
	}
	entry.UpdatedAt = result.Timestamp
	s.entries[id] = entry

	entryLogs := append([]model.LogEntry(nil), s.logs[id]...)
	entryLogs = append(entryLogs, model.LogEntry{
		Timestamp:  result.Timestamp,
		Trigger:    result.Trigger,
		Success:    result.Success,
		StatusCode: result.StatusCode,
		DurationMS: result.DurationMS,
		Message:    result.Message,
	})
	if len(entryLogs) > 10 {
		entryLogs = entryLogs[len(entryLogs)-10:]
	}
	s.logs[id] = entryLogs

	snapshot := s.snapshotLocked()
	s.mu.Unlock()

	return s.persist(snapshot)
}

func (s *Store) Logs(id string, limit int) []model.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	logs := append([]model.LogEntry(nil), s.logs[id]...)
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}

	if limit > 0 && len(logs) > limit {
		logs = logs[:limit]
	}

	return logs
}

func (s *Store) snapshotLocked() persistedState {
	entries := make([]model.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		entry.NextExecutionAt = time.Time{}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})

	logs := make(map[string][]model.LogEntry, len(s.logs))
	for id, entryLogs := range s.logs {
		logs[id] = append([]model.LogEntry(nil), entryLogs...)
	}

	return persistedState{Entries: entries, Logs: logs}
}

func (s *Store) persist(state persistedState) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}

	return nil
}
