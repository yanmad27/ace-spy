package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yanmad27/ace-spy/internal/server"
	"github.com/yanmad27/ace-spy/web"
)

func main() {
	addr := ":" + env("PORT", "8080")
	dbURL := firstEnv("DATABASE_URL", "POSTGRES_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL must be set")
	}
	token := os.Getenv("INGEST_TOKEN")
	if len(token) < 16 {
		log.Fatal("INGEST_TOKEN must be set (at least 16 characters)")
	}
	opts := server.Options{
		IngestToken:  token,
		ViewUser:     env("VIEW_USER", "admin"),
		ViewPassword: os.Getenv("VIEW_PASSWORD"),
		Dedupe:       time.Duration(envInt("DEDUPE_SECONDS", 600)) * time.Second,
		Retention:    time.Duration(envInt("RETENTION_DAYS", 30)) * 24 * time.Hour,
		CronSecret:   os.Getenv("CRON_SECRET"),
	}
	if opts.ViewPassword == "" {
		log.Print("WARNING: VIEW_PASSWORD not set — the viewer and read APIs are public")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	openCtx, cancelOpen := context.WithTimeout(ctx, 30*time.Second)
	store, err := server.OpenStore(openCtx, dbURL)
	cancelOpen()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// On Vercel, instances are suspended when idle, so retention runs via Vercel Cron instead.
	if os.Getenv("VERCEL") == "" && opts.Retention > 0 {
		go prune(ctx, store, opts.Retention)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(store, web.Static(), opts),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	go func() {
		log.Printf("ace-spy server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func prune(ctx context.Context, store *server.Store, retention time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		n, err := store.Prune(ctx, time.Now().Add(-retention).UnixMilli())
		if err != nil && ctx.Err() == nil {
			log.Printf("prune: %v", err)
		} else if n > 0 {
			log.Printf("pruned %d messages", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Fatalf("%s must be a non-negative integer", key)
	}
	return n
}
