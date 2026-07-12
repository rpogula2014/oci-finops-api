package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/costscope-api/internal/config"
	"github.com/example/costscope-api/internal/cost"
	"github.com/example/costscope-api/internal/httpapi"
)

func logLevel() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	repo, err := cost.NewClickHouseRepository(cfg.CHAddr, cfg.CHDatabase, cfg.CHUsername, cfg.CHPassword, cfg.CHSecure, cfg.CHDialTimeout)
	if err != nil {
		slog.Error("create ClickHouse client", "error", err)
		os.Exit(1)
	}
	service := cost.NewService(repo)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: httpapi.NewHandler(service, cfg.CHQueryTimeout).Routes(), ReadHeaderTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout}
	go func() {
		slog.Info("cost API listening", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "error", err)
			os.Exit(1)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		slog.Error("shutdown", "error", err)
	}
}
