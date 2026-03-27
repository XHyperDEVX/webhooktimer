package models

import (
    "time"
)

type TimerEntry struct {
    ID             string    `json:"id"`
    Name           string    `json:"name"`
    WebhookURL     string    `json:"webhookURL"`
    Mode           string    `json:"mode"` // fixed or random
    FixedInterval  int       `json:"fixedInterval"`
    MinInterval    int       `json:"minInterval"`
    MaxInterval    int       `json:"maxInterval"`
    Active         bool      `json:"active"`
    LastExecution  time.Time `json:"lastExecution"`
    WebhookTimeout int       `json:"webhookTimeout"`
    Method         string    `json:"method"`
    NextExecution  time.Time `json:"nextExecution"` // Only in RAM
}

type LogEntry struct {
    ID        int       `json:"id"`
    TimerID   string    `json:"timerId"`
    Timestamp time.Time `json:"timestamp"`
    Status    string    `json:"status"`
    Message   string    `json:"message"`
}
