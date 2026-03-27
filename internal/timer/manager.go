package timer

import (
    "context"
    "crypto/rand"
    "database/sql"
    "fmt"
    "log"
    "math/big"
    "net/http"
    "sync"
    "time"
    "webhooktimer/internal/models"
)

type Manager struct {
    mu       sync.RWMutex
    timers   map[string]*models.TimerEntry
    cancel   map[string]context.CancelFunc
    db       *sql.DB
    OnUpdate func(string) // To notify via WebSocket
}

func NewManager(db *sql.DB) *Manager {
    return &Manager{
        timers: make(map[string]*models.TimerEntry),
        cancel: make(map[string]context.CancelFunc),
        db:     db,
    }
}

func (m *Manager) StartAll() error {
    rows, err := m.db.Query("SELECT id, name, webhook_url, mode, fixed_interval, min_interval, max_interval, active, last_execution, webhook_timeout, method FROM timers")
    if err != nil {
        return err
    }
    defer rows.Close()

    for rows.Next() {
        var t models.TimerEntry
        var lastExec sql.NullTime
        err := rows.Scan(&t.ID, &t.Name, &t.WebhookURL, &t.Mode, &t.FixedInterval, &t.MinInterval, &t.MaxInterval, &t.Active, &lastExec, &t.WebhookTimeout, &t.Method)
        if err != nil {
            return err
        }
        if lastExec.Valid {
            t.LastExecution = lastExec.Time
        }
        m.mu.Lock()
        m.timers[t.ID] = &t
        m.mu.Unlock()

        if t.Active {
            m.startTimer(&t)
        }
    }
    return nil
}

func (m *Manager) startTimer(t *models.TimerEntry) {
    ctx, cancel := context.WithCancel(context.Background())
    m.mu.Lock()
    if oldCancel, ok := m.cancel[t.ID]; ok {
        oldCancel()
    }
    m.cancel[t.ID] = cancel
    m.mu.Unlock()

    go m.runTimer(ctx, t.ID)
}

func (m *Manager) StopTimer(id string) {
    m.mu.Lock()
    if cancel, ok := m.cancel[id]; ok {
        cancel()
        delete(m.cancel, id)
    }
    if t, ok := m.timers[id]; ok {
        t.Active = false
        t.NextExecution = time.Time{}
    }
    m.mu.Unlock()
}

func (m *Manager) UpdateTimer(t *models.TimerEntry) {
    m.mu.Lock()
    m.timers[t.ID] = t
    active := t.Active
    m.mu.Unlock()

    if active {
        m.startTimer(t)
    } else {
        m.StopTimer(t.ID)
    }
}

func (m *Manager) DeleteTimer(id string) {
    m.StopTimer(id)
    m.mu.Lock()
    delete(m.timers, id)
    m.mu.Unlock()
}

func (m *Manager) GetTimers() []*models.TimerEntry {
    m.mu.RLock()
    defer m.mu.RUnlock()
    res := make([]*models.TimerEntry, 0, len(m.timers))
    for _, t := range m.timers {
        res = append(res, t)
    }
    return res
}

func (m *Manager) runTimer(ctx context.Context, id string) {
    for {
        m.mu.RLock()
        t, ok := m.timers[id]
        m.mu.RUnlock()
        if !ok || !t.Active {
            return
        }

        interval := m.calculateInterval(t)
        t.NextExecution = time.Now().Add(interval)
        if m.OnUpdate != nil {
            m.OnUpdate(id)
        }

        select {
        case <-ctx.Done():
            return
        case <-time.After(interval):
            m.executeWebhook(t)
            if m.OnUpdate != nil {
                m.OnUpdate(id)
            }
        }
    }
}

func (m *Manager) calculateInterval(t *models.TimerEntry) time.Duration {
    if t.Mode == "fixed" {
        return time.Duration(t.FixedInterval) * time.Second
    }
    // random
    min := int64(t.MinInterval)
    max := int64(t.MaxInterval)
    if max <= min {
        return time.Duration(min) * time.Second
    }
    
    diff := max - min
    n, _ := rand.Int(rand.Reader, big.NewInt(diff+1))
    return time.Duration(min+n.Int64()) * time.Second
}

func (m *Manager) executeWebhook(t *models.TimerEntry) {
    client := &http.Client{
        Timeout: time.Duration(t.WebhookTimeout) * time.Second,
    }
    
    method := t.Method
    if method == "" {
        method = "POST"
    }
    
    req, err := http.NewRequest(method, t.WebhookURL, nil)
    if err != nil {
        log.Printf("Error creating request: %v", err)
        return
    }
    
    resp, err := client.Do(req)
    status := "success"
    message := ""
    if err != nil {
        status = "error"
        message = err.Error()
    } else {
        if resp.StatusCode < 200 || resp.StatusCode >= 300 {
            status = "error"
            message = fmt.Sprintf("HTTP Status %d", resp.StatusCode)
        }
        resp.Body.Close()
    }

    t.LastExecution = time.Now()
    
    // Update last execution in DB
    _, _ = m.db.Exec("UPDATE timers SET last_execution = ? WHERE id = ?", t.LastExecution, t.ID)
    
    // Add log entry
    _, _ = m.db.Exec("INSERT INTO logs (timer_id, timestamp, status, message) VALUES (?, ?, ?, ?)", t.ID, t.LastExecution, status, message)
    
    // Keep only last 3 logs
    _, _ = m.db.Exec("DELETE FROM logs WHERE timer_id = ? AND id NOT IN (SELECT id FROM logs WHERE timer_id = ? ORDER BY timestamp DESC LIMIT 3)", t.ID, t.ID)
    
    log.Printf("Executed webhook for %s: %s %s", t.Name, status, message)
}
