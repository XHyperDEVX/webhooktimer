package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"webhooktimer/internal/scheduler"
	"webhooktimer/internal/server"
	"webhooktimer/internal/store"

	_ "time/tzdata"
)

//go:embed web/index.html
var webFiles embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			log.Printf("healthcheck failed: %v", err)
			os.Exit(1)
		}
		return
	}

	port := envOrDefault("PORT", "8080")
	statePath := envOrDefault("STATE_PATH", "/data/state.json")

	location := time.Local
	timezoneName := strings.TrimSpace(os.Getenv("TZ"))
	if timezoneName != "" {
		loadedLocation, err := time.LoadLocation(timezoneName)
		if err != nil {
			log.Printf("invalid TZ %q, using system local timezone (%s)", timezoneName, location.String())
		} else {
			location = loadedLocation
		}
	}
	time.Local = location

	st := store.New(statePath)
	if err := st.Load(); err != nil {
		log.Fatalf("could not load state: %v", err)
	}

	sched := scheduler.New(st, location)
	sched.Start()
	defer sched.Shutdown()

	indexHTML, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("could not load UI: %v", err)
	}

	api := server.New(st, sched, indexHTML)
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           loggingMiddleware(api.Routes()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		log.Printf("webhooktimer listening on :%s (TZ=%s)", port, location.String())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server stopped: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

func runHealthcheck() error {
	port := envOrDefault("PORT", "8080")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
