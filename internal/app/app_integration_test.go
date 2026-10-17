//go:build integration

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/postgres"
)

func integrationConfig(t *testing.T) config.Config {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("Defina TEST_DATABASE_URL e prepare o PostgreSQL com as migrations")
	}
	return config.Config{
		DatabaseURL:            databaseURL,
		DatabaseConnectTimeout: time.Second,
		DatabaseMaxConns:       2,
	}
}

func TestPostgresLifecycleIntegration(t *testing.T) {
	cfg := integrationConfig(t)
	var pool *pgxpool.Pool
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	application := New(fx.Replace(cfg, logger), fx.Populate(&pool))
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := application.Stop(ctx); err != nil {
			t.Errorf("Erro no encerramento: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("Consulta no PostgreSQL real falhou: %v", err)
	}
	var hasWallets bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.wallets') IS NOT NULL").Scan(&hasWallets); err != nil || !hasWallets {
		t.Fatalf("Migration de wallets deve estar aplicada: %v", err)
	}
	if pool.Stat().MaxConns() != cfg.DatabaseMaxConns {
		t.Fatal("Pool não respeitou o limite configurado")
	}
	if err := application.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err == nil {
		t.Fatal("Pool deve estar fechado após Stop")
	}
	if !strings.Contains(logs.String(), "Pool do PostgreSQL encerrado") {
		t.Fatal("Encerramento deve ser observável nos logs")
	}
}

func TestPostgresRejectsBadCredentialsIntegration(t *testing.T) {
	cfg := integrationConfig(t)
	parsed, err := url.Parse(cfg.DatabaseURL)
	if err != nil || parsed.User == nil {
		t.Fatal("TEST_DATABASE_URL deve ser uma URL PostgreSQL com usuário")
	}
	const wrongPassword = "wrong-password-must-not-appear"
	parsed.User = url.UserPassword(parsed.User.Username(), wrongPassword)
	cfg.DatabaseURL = parsed.String()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	application := New(fx.Replace(cfg, logger))
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer application.Stop(ctx)
	if err := application.Start(ctx); !errors.Is(err, postgres.ErrUnavailable) {
		t.Fatalf("Esperava falha de autenticação no PostgreSQL real; recebeu %v", err)
	}
	if strings.Contains(logs.String(), wrongPassword) || strings.Contains(logs.String(), cfg.DatabaseURL) {
		t.Fatal("Logs de falha expuseram credenciais")
	}
}
