//go:build integration

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/health"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

func TestReadinessWithRealPostgresTimeoutAndRecoveryIntegration(t *testing.T) {
	cfg := integrationConfig(t)
	var pool *pgxpool.Pool
	var server *httpserver.Server
	application := New(isolatedAuth(), fx.Replace(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: time.Second}), fx.Populate(&pool, &server))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := application.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	client := &http.Client{Timeout: 4 * time.Second}
	request := func(path string, want int) health.Checks {
		t.Helper()
		resp, err := client.Get("http://" + server.Address() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Status string
			Checks health.Checks
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != want {
			t.Fatalf("%s retornou %d, esperado %d, erro=%v", path, resp.StatusCode, want, err)
		}
		return body.Checks
	}
	if got := request("/health/ready", 200); got.Postgres != health.OK || got.SQSConsumer != health.Disabled || got.SQSPublisher != health.Disabled {
		t.Fatal(got)
	}
	// Saturação do pool real: readiness respeita 2s; nenhuma query financeira é feita.
	first, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	started := time.Now()
	if got := request("/health/ready", 503); got.Postgres != health.Unavailable {
		t.Fatal(got)
	}
	if elapsed := time.Since(started); elapsed < health.Timeout || elapsed > health.Timeout+time.Second {
		t.Fatalf("prazo total incorreto: %s", elapsed)
	}
	request("/health/live", 200)
	first.Release()
	second.Release()
	if got := request("/health/ready", 200); !got.Ready() {
		t.Fatal("pool não recuperou prontidão", got)
	}
	pool.Close()
	if got := request("/health/ready", 503); got.Postgres != health.Unavailable {
		t.Fatal("pool fechado reportou prontidão", got)
	}
	request("/health/live", 200)
}
