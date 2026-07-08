package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"pg-crud/config"
	"pg-crud/handler"
	"pg-crud/metrics"
	"pg-crud/middleware"
	"pg-crud/repository"
	"pg-crud/tracing"
)

// fatal logs the startup error and exits: these run before the server
// accepts traffic, so there is nothing to shut down gracefully yet.
func fatal(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		fatal("config", err)
	}

	ctx := context.Background()

	tp, err := tracing.NewTracerProvider(ctx, cfg.OTelExporterEndpoint, "pg-crud")
	if err != nil {
		fatal("tracing", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown tracer provider", "error", err)
		}
	}()

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		fatal("parse pool config", err)
	}
	poolCfg.MaxConns = cfg.PoolMaxConns
	poolCfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		fatal("create pool", err)
	}
	defer pool.Close()

	if err := runMigrations(cfg.DatabaseURL); err != nil {
		fatal("migrations", err)
	}

	redisClient, err := repository.NewRedisClient(ctx, repository.RedisConfig{
		Addr:         cfg.RedisAddr,
		PoolSize:     cfg.RedisPoolSize,
		MinIdleConns: cfg.RedisMinIdleConns,
	})
	if err != nil {
		fatal("redis", err)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			slog.Error("close redis client", "error", err)
		}
	}()

	cacheMetrics := metrics.NewCacheMetrics(prometheus.DefaultRegisterer)
	httpMetrics := metrics.NewHTTPMetrics(prometheus.DefaultRegisterer)

	pgRepo := repository.NewUserRepository(pool)
	cachedRepo := repository.NewCachedUserRepository(pgRepo, redisClient, cfg.CacheTTL, repository.BreakerConfig{
		Threshold: cfg.BreakerThreshold,
		Cooldown:  cfg.BreakerCooldown,
	}, cacheMetrics)

	userHandler := handler.NewUserHandler(cachedRepo)

	if len(cfg.APIKeys) == 0 {
		slog.Warn("API_KEYS not set: /users endpoints are unauthenticated")
	}
	auth := middleware.Authenticate(cfg.APIKeys)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", auth(userHandler.Create))
	mux.HandleFunc("GET /users", auth(userHandler.List))
	mux.HandleFunc("GET /users/{id}", auth(userHandler.GetByID))
	mux.HandleFunc("PUT /users/{id}", auth(userHandler.Update))
	mux.HandleFunc("DELETE /users/{id}", auth(userHandler.Delete))
	mux.Handle("/metrics", promhttp.Handler())

	// Liveness: the process is up and serving. No dependency checks —
	// restarting the pod won't fix a broken Postgres.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: Postgres is mandatory (source of truth), Redis is not —
	// the cache layer fails open, so a degraded cache must not pull the
	// instance out of the load balancer.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := pool.Ping(checkCtx); err != nil {
			writeHealth(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "postgres": "down"})
			return
		}
		cache := "ok"
		if err := redisClient.Ping(checkCtx).Err(); err != nil {
			cache = "degraded"
		}
		writeHealth(w, http.StatusOK, map[string]string{"status": "ok", "postgres": "ok", "cache": cache})
	})

	srv := &http.Server{
		Addr:              cfg.ServerAddr,
		Handler:           middleware.Instrument(mux, httpMetrics),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", cfg.ServerAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("listen", err)
		}
	}()

	<-sigCtx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown", "error", err)
	}
}

func writeHealth(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write health response", "error", err)
	}
}

func runMigrations(dsn string) error {
	m, err := migrate.New("file://migrations", dsn)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil || dbErr != nil {
			slog.Warn("migrate close", "source_error", srcErr, "db_error", dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
