package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"webhooktimer/internal/handlers"
	"webhooktimer/internal/models"
	"webhooktimer/internal/timer"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/data/timers.db"
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatal(err)
	}

	if err := models.InitDB(dbPath); err != nil {
		log.Fatal(err)
	}

	manager := timer.NewManager(models.DB)
	if err := manager.StartAll(); err != nil {
		log.Fatal(err)
	}

	h := handlers.NewHandler(manager)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/api/timers", h.GetTimers)
	r.Post("/api/timers", h.CreateTimer)
	r.Put("/api/timers/{id}", h.UpdateTimer)
	r.Delete("/api/timers/{id}", h.DeleteTimer)
	r.Post("/api/timers/{id}/toggle", h.ToggleTimer)
	r.Get("/api/timers/{id}/logs", h.GetLogs)
	r.HandleFunc("/ws", h.HandleWS)

	// Serve static files
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/templates/index.html")
	})
	
	// Create a file server for static assets
	staticDir := http.Dir("web/static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(staticDir)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatal(err)
	}
}
