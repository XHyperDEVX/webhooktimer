package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"webhooktimer/internal/model"
	"webhooktimer/internal/scheduler"
	"webhooktimer/internal/store"
)

const defaultLogLimit = 15

type Server struct {
	store     *store.Store
	scheduler *scheduler.Manager
	indexHTML []byte
}

type entryPayload struct {
	Name           string     `json:"name"`
	WebhookURL     string     `json:"webhookURL"`
	Method         string     `json:"method"`
	Mode           model.Mode `json:"mode"`
	FixedSeconds   int        `json:"fixedSeconds"`
	RandomMin      int        `json:"randomMin"`
	RandomMax      int        `json:"randomMax"`
	SleepEnabled   bool       `json:"sleepEnabled"`
	SleepStart     string     `json:"sleepStart"`
	SleepEnd       string     `json:"sleepEnd"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Active         *bool      `json:"active"`
}

type executeResponse struct {
	Success    bool      `json:"success"`
	StatusCode int       `json:"statusCode"`
	DurationMS int64     `json:"durationMs"`
	Message    string    `json:"message"`
	ExecutedAt time.Time `json:"executedAt"`
}

type settingsPayload struct {
	DiscordWebhookURL string `json:"discordWebhookURL"`
}

type discordTestPayload struct {
	DiscordWebhookURL string `json:"discordWebhookURL"`
}

func New(st *store.Store, sched *scheduler.Manager, indexHTML []byte) *Server {
	return &Server{store: st, scheduler: sched, indexHTML: indexHTML}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/time", s.handleTime)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/settings/discord/test", s.handleDiscordTest)
	mux.HandleFunc("/api/entries", s.handleEntries)
	mux.HandleFunc("/api/entries/", s.handleEntryByID)

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.indexHTML)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"unixMs":   now.UnixMilli(),
		"time":     now.Format("15:04:05"),
		"timezone": now.Location().String(),
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.Settings())
	case http.MethodPut:
		var payload settingsPayload
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		webhookURL := strings.TrimSpace(payload.DiscordWebhookURL)
		if webhookURL != "" {
			if err := validateWebhookURL(webhookURL); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		if err := s.store.SetDiscordWebhookURL(webhookURL); err != nil {
			writeError(w, http.StatusInternalServerError, "could not update settings")
			return
		}

		writeJSON(w, http.StatusOK, s.store.Settings())
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleDiscordTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var payload discordTestPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	webhookURL := strings.TrimSpace(payload.DiscordWebhookURL)
	if webhookURL == "" {
		writeError(w, http.StatusBadRequest, "discordWebhookURL is required")
		return
	}
	if err := validateWebhookURL(webhookURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.scheduler.SendDiscordTest(webhookURL); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleEntries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.APIEntries(defaultLogLimit))
	case http.MethodPost:
		var payload entryPayload
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		entry, err := entryFromPayload(payload, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		entry.ID = generateID()

		if err := s.store.UpsertEntry(entry); err != nil {
			writeError(w, http.StatusInternalServerError, "could not store entry")
			return
		}
		s.scheduler.SyncEntry(entry.ID)

		stored, _ := s.store.GetEntry(entry.ID)
		writeJSON(w, http.StatusCreated, model.ToAPIEntry(stored, s.store.Logs(entry.ID, defaultLogLimit)))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleEntryByID(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseEntryPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch action {
	case "":
		s.handleEntryCRUD(w, r, id)
	case "toggle":
		s.handleToggle(w, r, id)
	case "execute":
		s.handleExecute(w, r, id)
	case "logs":
		s.handleLogs(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleEntryCRUD(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut:
		existing, ok := s.store.GetEntry(id)
		if !ok {
			writeError(w, http.StatusNotFound, "entry not found")
			return
		}

		var payload entryPayload
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		entry, err := entryFromPayload(payload, existing.Active)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		entry.ID = id

		if err := s.store.UpsertEntry(entry); err != nil {
			writeError(w, http.StatusInternalServerError, "could not update entry")
			return
		}
		s.scheduler.SyncEntry(id)

		stored, _ := s.store.GetEntry(id)
		writeJSON(w, http.StatusOK, model.ToAPIEntry(stored, s.store.Logs(id, defaultLogLimit)))
	case http.MethodDelete:
		if err := s.store.DeleteEntry(id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "entry not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not delete entry")
			return
		}
		s.scheduler.RemoveEntry(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		Active bool `json:"active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.SetActive(id, body.Active); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update entry")
		return
	}
	s.scheduler.SyncEntry(id)

	entry, _ := s.store.GetEntry(id)
	writeJSON(w, http.StatusOK, model.ToAPIEntry(entry, s.store.Logs(id, defaultLogLimit)))
}

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	result, err := s.scheduler.ExecuteNow(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "execution failed")
		return
	}

	writeJSON(w, http.StatusOK, executeResponse{
		Success:    result.Success,
		StatusCode: result.StatusCode,
		DurationMS: result.DurationMS,
		Message:    result.Message,
		ExecutedAt: result.Timestamp,
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if _, ok := s.store.GetEntry(id); !ok {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}

	limit := defaultLogLimit
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}

	writeJSON(w, http.StatusOK, s.store.Logs(id, limit))
}

func entryFromPayload(payload entryPayload, defaultActive bool) (model.Entry, error) {
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return model.Entry{}, errors.New("name is required")
	}

	webhook := strings.TrimSpace(payload.WebhookURL)
	if webhook == "" {
		return model.Entry{}, errors.New("webhookURL is required")
	}
	if err := validateWebhookURL(webhook); err != nil {
		return model.Entry{}, errors.New("webhookURL must be a valid http/https URL")
	}

	method := strings.ToUpper(strings.TrimSpace(payload.Method))
	if method == "" {
		method = http.MethodPost
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return model.Entry{}, errors.New("unsupported method")
	}

	mode := payload.Mode
	if mode == "" {
		mode = model.ModeFixed
	}
	if mode != model.ModeFixed && mode != model.ModeRandom {
		return model.Entry{}, errors.New("mode must be fixed or random")
	}

	fixed := payload.FixedSeconds
	randomMin := payload.RandomMin
	randomMax := payload.RandomMax

	switch mode {
	case model.ModeFixed:
		if fixed < 1 {
			return model.Entry{}, errors.New("fixedSeconds must be at least 1")
		}
	case model.ModeRandom:
		if randomMin < 1 || randomMax < 1 {
			return model.Entry{}, errors.New("random intervals must be at least 1")
		}
		if randomMax < randomMin {
			return model.Entry{}, errors.New("randomMax must be greater than or equal to randomMin")
		}
	}

	timeout := payload.TimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}
	if timeout < 1 || timeout > 600 {
		return model.Entry{}, errors.New("timeoutSeconds must be between 1 and 600")
	}

	sleepStart := ""
	sleepEnd := ""
	if payload.SleepEnabled {
		sleepStart = strings.TrimSpace(payload.SleepStart)
		sleepEnd = strings.TrimSpace(payload.SleepEnd)
		if !validHHMM(sleepStart) || !validHHMM(sleepEnd) {
			return model.Entry{}, errors.New("sleep times must be in HH:MM format")
		}
	}

	active := defaultActive
	if payload.Active != nil {
		active = *payload.Active
	}

	return model.Entry{
		Name:           name,
		WebhookURL:     webhook,
		Method:         method,
		Mode:           mode,
		FixedSeconds:   fixed,
		RandomMin:      randomMin,
		RandomMax:      randomMax,
		SleepEnabled:   payload.SleepEnabled,
		SleepStart:     sleepStart,
		SleepEnd:       sleepEnd,
		TimeoutSeconds: timeout,
		Active:         active,
	}, nil
}

func validateWebhookURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("must be a valid http/https URL")
	}
	return nil
}

func validHHMM(value string) bool {
	if len(value) != 5 {
		return false
	}
	_, err := time.Parse("15:04", value)
	return err == nil
}

func parseEntryPath(path string) (id string, action string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/api/entries/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", "", false
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 {
		return parts[0], "", true
	}
	if len(parts) == 2 {
		return parts[0], parts[1], true
	}

	return "", "", false
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func generateID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	}
	return hex.EncodeToString(bytes)
}
