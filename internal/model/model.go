package model

import "time"

type Mode string

const (
	ModeFixed  Mode = "fixed"
	ModeRandom Mode = "random"
)

type Entry struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	WebhookURL      string    `json:"webhookURL"`
	Method          string    `json:"method"`
	Mode            Mode      `json:"mode"`
	FixedSeconds    int       `json:"fixedSeconds"`
	RandomMin       int       `json:"randomMin"`
	RandomMax       int       `json:"randomMax"`
	SleepEnabled    bool      `json:"sleepEnabled"`
	SleepStart      string    `json:"sleepStart"`
	SleepEnd        string    `json:"sleepEnd"`
	TimeoutSeconds  int       `json:"timeoutSeconds"`
	Active          bool      `json:"active"`
	LastExecution   time.Time `json:"lastExecution"`
	LastResult      string    `json:"lastResult"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	NextExecutionAt time.Time `json:"-"`
}

type LogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Trigger    string    `json:"trigger"`
	Success    bool      `json:"success"`
	StatusCode int       `json:"statusCode"`
	DurationMS int64     `json:"durationMs"`
	Message    string    `json:"message"`
}

type Settings struct {
	DiscordWebhookURL string `json:"discordWebhookURL"`
}

type ExecuteResult struct {
	Timestamp  time.Time
	Trigger    string
	Success    bool
	StatusCode int
	DurationMS int64
	Message    string
}

type APIEntry struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	WebhookURL     string     `json:"webhookURL"`
	Method         string     `json:"method"`
	Mode           Mode       `json:"mode"`
	FixedSeconds   int        `json:"fixedSeconds"`
	RandomMin      int        `json:"randomMin"`
	RandomMax      int        `json:"randomMax"`
	SleepEnabled   bool       `json:"sleepEnabled"`
	SleepStart     string     `json:"sleepStart"`
	SleepEnd       string     `json:"sleepEnd"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Active         bool       `json:"active"`
	LastExecution  *time.Time `json:"lastExecution"`
	LastResult     string     `json:"lastResult"`
	NextExecution  *time.Time `json:"nextExecution"`
	Logs           []LogEntry `json:"logs"`
}

func ToAPIEntry(entry Entry, logs []LogEntry) APIEntry {
	api := APIEntry{
		ID:             entry.ID,
		Name:           entry.Name,
		WebhookURL:     entry.WebhookURL,
		Method:         entry.Method,
		Mode:           entry.Mode,
		FixedSeconds:   entry.FixedSeconds,
		RandomMin:      entry.RandomMin,
		RandomMax:      entry.RandomMax,
		SleepEnabled:   entry.SleepEnabled,
		SleepStart:     entry.SleepStart,
		SleepEnd:       entry.SleepEnd,
		TimeoutSeconds: entry.TimeoutSeconds,
		Active:         entry.Active,
		LastResult:     entry.LastResult,
		Logs:           logs,
	}

	if !entry.LastExecution.IsZero() {
		last := entry.LastExecution
		api.LastExecution = &last
	}
	if !entry.NextExecutionAt.IsZero() {
		next := entry.NextExecutionAt
		api.NextExecution = &next
	}

	return api
}
