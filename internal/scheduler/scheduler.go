package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
	"webhooktimer/internal/model"
	"webhooktimer/internal/store"
)

type Manager struct {
	store      *store.Store
	location   *time.Location
	httpClient *http.Client

	jobsMu sync.Mutex
	jobs   map[string]context.CancelFunc

	execMu sync.Mutex
	exec   map[string]*sync.Mutex
}

func New(st *store.Store, location *time.Location) *Manager {
	return &Manager{
		store:    st,
		location: location,
		httpClient: &http.Client{
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		jobs: make(map[string]context.CancelFunc),
		exec: make(map[string]*sync.Mutex),
	}
}

func (m *Manager) Start() {
	for _, entry := range m.store.ListEntries() {
		if entry.Active {
			m.startJob(entry.ID)
		}
	}
}

func (m *Manager) Shutdown() {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()

	for id, cancel := range m.jobs {
		cancel()
		_ = m.store.SetNextExecution(id, time.Time{})
	}
	m.jobs = make(map[string]context.CancelFunc)
}

func (m *Manager) SyncEntry(id string) {
	m.stopJob(id)

	entry, ok := m.store.GetEntry(id)
	if !ok || !entry.Active {
		return
	}

	m.startJob(id)
}

func (m *Manager) RemoveEntry(id string) {
	m.stopJob(id)
	m.execMu.Lock()
	delete(m.exec, id)
	m.execMu.Unlock()
}

func (m *Manager) ExecuteNow(id string) (model.ExecuteResult, error) {
	entry, ok := m.store.GetEntry(id)
	if !ok {
		return model.ExecuteResult{}, store.ErrNotFound
	}

	result := m.execute(entry, "manual")
	if err := m.store.RecordExecution(id, result); err != nil {
		return result, err
	}

	return result, nil
}

func (m *Manager) startJob(id string) {
	ctx, cancel := context.WithCancel(context.Background())

	m.jobsMu.Lock()
	if existing, ok := m.jobs[id]; ok {
		existing()
	}
	m.jobs[id] = cancel
	m.jobsMu.Unlock()

	go m.runJob(ctx, id)
}

func (m *Manager) stopJob(id string) {
	m.jobsMu.Lock()
	if cancel, ok := m.jobs[id]; ok {
		cancel()
		delete(m.jobs, id)
	}
	m.jobsMu.Unlock()

	_ = m.store.SetNextExecution(id, time.Time{})
}

func (m *Manager) runJob(ctx context.Context, id string) {
	for {
		entry, ok := m.store.GetEntry(id)
		if !ok || !entry.Active {
			_ = m.store.SetNextExecution(id, time.Time{})
			return
		}

		next := m.nextExecution(entry)
		_ = m.store.SetNextExecution(id, next)

		wait := time.Until(next)
		if wait < time.Second {
			wait = time.Second
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = m.store.SetNextExecution(id, time.Time{})
			return
		case <-timer.C:
		}

		entry, ok = m.store.GetEntry(id)
		if !ok || !entry.Active {
			continue
		}

		result := m.execute(entry, "scheduled")
		if err := m.store.RecordExecution(id, result); err != nil && !errors.Is(err, store.ErrNotFound) {
			continue
		}
	}
}

func (m *Manager) nextExecution(entry model.Entry) time.Time {
	now := time.Now().In(m.location)
	interval := m.pickInterval(entry)
	candidate := now.Add(interval)

	if entry.SleepEnabled {
		start, end, ok := parseHHMM(entry.SleepStart, entry.SleepEnd)
		if ok && start != end {
			minutes := candidate.Hour()*60 + candidate.Minute()
			if isInSleepWindow(minutes, start, end) {
				candidate = sleepEndTime(candidate, start, end)
			}
		}
	}

	if candidate.Before(now.Add(time.Second)) {
		candidate = now.Add(time.Second)
	}

	return candidate.UTC()
}

func (m *Manager) pickInterval(entry model.Entry) time.Duration {
	if entry.Mode == model.ModeRandom {
		min := entry.RandomMin
		max := entry.RandomMax
		if min < 1 {
			min = 1
		}
		if max < min {
			max = min
		}
		return time.Duration(min+rand.IntN(max-min+1)) * time.Second
	}

	seconds := entry.FixedSeconds
	if seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}

func (m *Manager) execute(entry model.Entry, trigger string) model.ExecuteResult {
	lock := m.execLock(entry.ID)
	lock.Lock()
	defer lock.Unlock()

	timeout := time.Duration(entry.TimeoutSeconds) * time.Second
	if timeout < time.Second {
		timeout = 10 * time.Second
	}

	start := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, entry.Method, entry.WebhookURL, nil)
	if err != nil {
		return model.ExecuteResult{
			Timestamp:  start,
			Trigger:    trigger,
			Success:    false,
			StatusCode: 0,
			DurationMS: 0,
			Message:    err.Error(),
		}
	}
	req.Header.Set("User-Agent", "webhooktimer/2")

	resp, err := m.httpClient.Do(req)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return model.ExecuteResult{
			Timestamp:  time.Now().UTC(),
			Trigger:    trigger,
			Success:    false,
			StatusCode: 0,
			DurationMS: duration,
			Message:    err.Error(),
		}
	}
	defer resp.Body.Close()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	message := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if !success {
		message = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	return model.ExecuteResult{
		Timestamp:  time.Now().UTC(),
		Trigger:    trigger,
		Success:    success,
		StatusCode: resp.StatusCode,
		DurationMS: duration,
		Message:    message,
	}
}

func (m *Manager) execLock(id string) *sync.Mutex {
	m.execMu.Lock()
	defer m.execMu.Unlock()

	if lock, ok := m.exec[id]; ok {
		return lock
	}

	lock := &sync.Mutex{}
	m.exec[id] = lock
	return lock
}

func parseHHMM(start string, end string) (int, int, bool) {
	sh, sm, ok := parseOneHHMM(start)
	if !ok {
		return 0, 0, false
	}
	eh, em, ok := parseOneHHMM(end)
	if !ok {
		return 0, 0, false
	}
	return sh*60 + sm, eh*60 + em, true
}

func parseOneHHMM(value string) (int, int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(value, "%02d:%02d", &h, &m); err != nil {
		return 0, 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func isInSleepWindow(minutes int, start int, end int) bool {
	if start < end {
		return minutes >= start && minutes < end
	}
	return minutes >= start || minutes < end
}

func sleepEndTime(t time.Time, start int, end int) time.Time {
	y, m, d := t.Date()
	loc := t.Location()
	endToday := time.Date(y, m, d, end/60, end%60, 0, 0, loc)

	if start < end {
		return endToday
	}

	minutes := t.Hour()*60 + t.Minute()
	if minutes >= start {
		return endToday.Add(24 * time.Hour)
	}

	return endToday
}
