package handlers

import (
    "encoding/json"
    "net/http"
    "sync"
    "webhooktimer/internal/models"
    "webhooktimer/internal/timer"

    "github.com/go-chi/chi/v5"
    "github.com/google/uuid"
    "github.com/gorilla/websocket"
)

type Handler struct {
    Manager *timer.Manager
    clients map[*websocket.Conn]bool
    mu      sync.Mutex
}

func NewHandler(m *timer.Manager) *Handler {
    h := &Handler{
        Manager: m,
        clients: make(map[*websocket.Conn]bool),
    }
    m.OnUpdate = h.broadcastTimers
    return h
}

var upgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool { return true },
}

func (h *Handler) broadcastTimers(_ string) {
    h.mu.Lock()
    defer h.mu.Unlock()
    timers := h.Manager.GetTimers()
    for client := range h.clients {
        err := client.WriteJSON(timers)
        if err != nil {
            client.Close()
            delete(h.clients, client)
        }
    }
}

func (h *Handler) GetTimers(w http.ResponseWriter, r *http.Request) {
    timers := h.Manager.GetTimers()
    json.NewEncoder(w).Encode(timers)
}

func (h *Handler) CreateTimer(w http.ResponseWriter, r *http.Request) {
    var t models.TimerEntry
    if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    t.ID = uuid.New().String()
    if t.WebhookTimeout == 0 {
        t.WebhookTimeout = 5
    }
    if t.Method == "" {
        t.Method = "POST"
    }

    if t.Type == "" {
        t.Type = "other"
    }

    _, err := models.DB.Exec("INSERT INTO timers (id, name, webhook_url, mode, fixed_interval, min_interval, max_interval, active, webhook_timeout, method, type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        t.ID, t.Name, t.WebhookURL, t.Mode, t.FixedInterval, t.MinInterval, t.MaxInterval, t.Active, t.WebhookTimeout, t.Method, t.Type)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    h.Manager.UpdateTimer(&t)
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(t)
}

func (h *Handler) UpdateTimer(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    var t models.TimerEntry
    if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    t.ID = id

    _, err := models.DB.Exec("UPDATE timers SET name = ?, webhook_url = ?, mode = ?, fixed_interval = ?, min_interval = ?, max_interval = ?, active = ?, webhook_timeout = ?, method = ?, type = ? WHERE id = ?",
        t.Name, t.WebhookURL, t.Mode, t.FixedInterval, t.MinInterval, t.MaxInterval, t.Active, t.WebhookTimeout, t.Method, t.Type, id)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    h.Manager.UpdateTimer(&t)
    json.NewEncoder(w).Encode(t)
}

func (h *Handler) DeleteTimer(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    _, err := models.DB.Exec("DELETE FROM timers WHERE id = ?", id)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    h.Manager.DeleteTimer(id)
    h.broadcastTimers("")
    w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CallNow(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    h.Manager.CallNow(id)
    w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ToggleTimer(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    var body struct {
        Active bool `json:"active"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    _, err := models.DB.Exec("UPDATE timers SET active = ? WHERE id = ?", body.Active, id)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    // Fetch updated timer
    var t models.TimerEntry
    row := models.DB.QueryRow("SELECT id, name, webhook_url, mode, fixed_interval, min_interval, max_interval, active, last_execution, webhook_timeout, method, type FROM timers WHERE id = ?", id)
    err = row.Scan(&t.ID, &t.Name, &t.WebhookURL, &t.Mode, &t.FixedInterval, &t.MinInterval, &t.MaxInterval, &t.Active, &t.LastExecution, &t.WebhookTimeout, &t.Method, &t.Type)
    if err == nil {
        h.Manager.UpdateTimer(&t)
    }

    w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetLogs(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    rows, err := models.DB.Query("SELECT id, timer_id, timestamp, status, message FROM logs WHERE timer_id = ? ORDER BY timestamp DESC LIMIT 3", id)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer rows.Close()

    logs := []models.LogEntry{}
    for rows.Next() {
        var l models.LogEntry
        if err := rows.Scan(&l.ID, &l.TimerID, &l.Timestamp, &l.Status, &l.Message); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        logs = append(logs, l)
    }
    json.NewEncoder(w).Encode(logs)
}

func (h *Handler) HandleWS(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        return
    }
    h.mu.Lock()
    h.clients[conn] = true
    h.mu.Unlock()

    defer func() {
        h.mu.Lock()
        delete(h.clients, conn)
        h.mu.Unlock()
        conn.Close()
    }()

    // Initial push
    timers := h.Manager.GetTimers()
    if err := conn.WriteJSON(timers); err != nil {
        return
    }

    // Keep connection open
    for {
        if _, _, err := conn.ReadMessage(); err != nil {
            break
        }
    }
}
