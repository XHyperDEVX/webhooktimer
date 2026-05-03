package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
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
	if location == nil {
		location = time.Local
	}

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

	m.notifyFailure(entry, result)

	return result, nil
}

func (m *Manager) SendDiscordTest(webhookURL string) error {
	now := time.Now().In(m.location)
	payload := discordWebhookPayload{
		Username: "Webhook Timer",
		Embeds: []discordEmbed{
			{
				Title:       "Webhook Timer test notification",
				Description: "This is a test embed from Webhook Timer.",
				Color:       5763719,
				Timestamp:   now.UTC().Format(time.RFC3339),
				Fields: []discordEmbedField{
					{Name: "Timezone", Value: m.location.String(), Inline: true},
					{Name: "Time", Value: now.Format("2006-01-02 15:04:05"), Inline: true},
				},
			},
		},
	}

	return m.sendDiscordWebhook(webhookURL, payload)
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

		now := time.Now().In(m.location)
		next := m.nextExecutionFrom(entry, now)
		_ = m.store.SetNextExecution(id, next.UTC())

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

		if m.isSleepModeActive(entry, time.Now().In(m.location)) {
			result := model.ExecuteResult{
				Timestamp:  time.Now().UTC(),
				Trigger:    "scheduled",
				Success:    false,
				StatusCode: 0,
				DurationMS: 0,
				Message:    "Skipped: sleep window active",
			}
			if err := m.store.RecordExecution(id, result); err != nil && !errors.Is(err, store.ErrNotFound) {
				continue
			}
			continue
		}

		result := m.execute(entry, "scheduled")
		if err := m.store.RecordExecution(id, result); err != nil && !errors.Is(err, store.ErrNotFound) {
			continue
		}

		m.notifyFailure(entry, result)
	}
}

func (m *Manager) nextExecution(entry model.Entry) time.Time {
	now := time.Now().In(m.location)
	return m.nextExecutionFrom(entry, now)
}

func (m *Manager) nextExecutionFrom(entry model.Entry, now time.Time) time.Time {
	candidate := now.In(m.location).Add(m.pickInterval(entry))

	minTime := now.In(m.location).Add(time.Second)
	if candidate.Before(minTime) {
		candidate = minTime
	}

	return m.shiftOutOfSleepWindow(entry, candidate)
}

func (m *Manager) shiftOutOfSleepWindow(entry model.Entry, candidate time.Time) time.Time {
	if !entry.SleepEnabled {
		return candidate
	}

	startMinutes, endMinutes, ok := parseHHMM(entry.SleepStart, entry.SleepEnd)
	if !ok || startMinutes == endMinutes {
		return candidate
	}

	startSeconds := startMinutes * 60
	endSeconds := endMinutes * 60

	for isInSleepWindow(secondOfDay(candidate), startSeconds, endSeconds) {
		candidate = sleepWindowEnd(candidate, startMinutes, endMinutes)
	}

	return candidate
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

	return model.ExecuteResult{
		Timestamp:  time.Now().UTC(),
		Trigger:    trigger,
		Success:    success,
		StatusCode: resp.StatusCode,
		DurationMS: duration,
		Message:    message,
	}
}

func (m *Manager) notifyFailure(entry model.Entry, result model.ExecuteResult) {
	if result.Success {
		return
	}

	if strings.HasPrefix(strings.ToLower(result.Message), "skipped:") {
		return
	}

	settings := m.store.Settings()
	webhookURL := strings.TrimSpace(settings.DiscordWebhookURL)
	if webhookURL == "" {
		return
	}

	now := time.Now().In(m.location)
	payload := discordWebhookPayload{
		Username: "Webhook Timer",
		Embeds: []discordEmbed{
			{
				Title:       "Webhook execution failed",
				Description: fmt.Sprintf("Entry **%s** failed to execute.", entry.Name),
				Color:       15548997,
				Timestamp:   now.UTC().Format(time.RFC3339),
				Fields: []discordEmbedField{
					{Name: "Entry", Value: entry.Name, Inline: true},
					{Name: "Trigger", Value: result.Trigger, Inline: true},
					{Name: "Method", Value: entry.Method, Inline: true},
					{Name: "Status", Value: fmt.Sprintf("%d", result.StatusCode), Inline: true},
					{Name: "Duration", Value: fmt.Sprintf("%dms", result.DurationMS), Inline: true},
					{Name: "Message", Value: truncateForDiscord(result.Message, 900), Inline: false},
					{Name: "URL", Value: truncateForDiscord(entry.WebhookURL, 900), Inline: false},
				},
			},
		},
	}

	_ = m.sendDiscordWebhook(webhookURL, payload)
}

func (m *Manager) sendDiscordWebhook(webhookURL string, payload discordWebhookPayload) error {
	target := strings.TrimSpace(webhookURL)
	if target == "" {
		return errors.New("discordWebhookURL is required")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "webhooktimer/2")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if len(raw) == 0 {
			return fmt.Errorf("discord webhook returned status %d", resp.StatusCode)
		}
		return fmt.Errorf("discord webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return nil
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

func (m *Manager) isSleepModeActive(entry model.Entry, now time.Time) bool {
	if !entry.SleepEnabled {
		return false
	}

	startMinutes, endMinutes, ok := parseHHMM(entry.SleepStart, entry.SleepEnd)
	if !ok || startMinutes == endMinutes {
		return false
	}

	return isInSleepWindow(secondOfDay(now), startMinutes*60, endMinutes*60)
}

func secondOfDay(value time.Time) int {
	return value.Hour()*3600 + value.Minute()*60 + value.Second()
}

func sleepWindowEnd(now time.Time, startMinutes int, endMinutes int) time.Time {
	loc := now.Location()

	endToday := time.Date(
		now.Year(),
		now.Month(),
		now.Day(),
		endMinutes/60,
		endMinutes%60,
		0,
		0,
		loc,
	)

	if startMinutes < endMinutes {
		return endToday
	}

	if now.Before(endToday) {
		return endToday
	}

	return endToday.Add(24 * time.Hour)
}

func isInSleepWindow(current int, start int, end int) bool {
	if start < end {
		return current >= start && current < end
	}
	return current >= start || current < end
}

type discordWebhookPayload struct {
	Username string         `json:"username,omitempty"`
	Embeds   []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

func truncateForDiscord(value string, limit int) string {
	if limit < 1 {
		return ""
	}

	if len(value) <= limit {
		return value
	}

	if limit < 4 {
		return value[:limit]
	}

	return value[:limit-3] + "..."
}
