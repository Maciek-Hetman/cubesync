package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/db/migrations"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration_error", "error", err)
		os.Exit(1)
	}

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "serve":
		if err := serve(cfg, logger); err != nil {
			logger.Error("server_stopped", "error", err)
			os.Exit(1)
		}
	case "migrate":
		if err := migrate(cfg.DatabaseURL); err != nil {
			logger.Error("migration_failed", "error", err)
			os.Exit(1)
		}
		logger.Info("migration_complete")
	case "create-admin":
		if err := runCreateAdmin(cfg, os.Args[2:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
	case "healthcheck":
		if err := healthcheck(cfg.HTTPAddress); err != nil {
			logger.Error("healthcheck_failed", "error", err)
			os.Exit(1)
		}
	default:
		logger.Error("unknown_command", "command", command)
		os.Exit(2)
	}
}

func serve(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	poolConfig.MaxConns = 20
	poolConfig.MinConns = 4
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           httpapi.NewRouter(cfg, pool, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errs := make(chan error, 1)
	go func() {
		logger.Info("server_started", "address", cfg.HTTPAddress)
		errs <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func migrate(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func healthcheck(address string) error {
	client := http.Client{Timeout: 2 * time.Second}
	port := address
	if idx := strings.LastIndex(address, ":"); idx != -1 {
		port = address[idx:]
	}
	response, err := client.Get("http://127.0.0.1" + port + "/health/ready")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("readiness endpoint returned a non-200 response")
	}
	return nil
}
