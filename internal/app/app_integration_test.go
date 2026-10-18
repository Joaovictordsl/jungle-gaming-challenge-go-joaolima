//go:build integration

package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
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

// Estes testes isolam o ciclo de vida HTTP/PostgreSQL. O OIDC real é exercitado
// separadamente nos testes de simulação e Keycloak.
type unavailableTokenVerifier struct{}

func (unavailableTokenVerifier) Verify(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.ErrUnavailable
}

func isolatedAuth() fx.Option {
	return fx.Replace(fx.Annotate(unavailableTokenVerifier{}, fx.As(new(auth.TokenVerifier))))
}

func TestPostgresLifecycleIntegration(t *testing.T) {
	cfg := integrationConfig(t)
	var pool *pgxpool.Pool
	var repository wallet.Repository
	var openWallet *applicationwallet.OpenWallet
	var server *httpserver.Server
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	httpConfig := config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: time.Second}
	application := New(isolatedAuth(), fx.Replace(cfg, logger, httpConfig), fx.Populate(&pool, &repository, &openWallet, &server))
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
	client := &nethttp.Client{Timeout: time.Second}
	response, err := client.Get("http://" + server.Address() + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != nethttp.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("Servidor da aplicação deve responder ao health check: %s, %v", body, err)
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
	probeID := "fx-repository-probe-" + time.Now().UTC().Format("20060102T150405.000000000")
	if _, err := repository.FindByID(ctx, probeID); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatalf("Repositório fornecido pelo Fx deve consultar o PostgreSQL: %v", err)
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
	httpStop := strings.Index(logs.String(), "Servidor HTTP encerrado")
	poolStop := strings.Index(logs.String(), "Pool do PostgreSQL encerrado")
	if httpStop < 0 || httpStop > poolStop {
		t.Fatal("Servidor HTTP deve encerrar antes do pool do PostgreSQL")
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
	application := New(isolatedAuth(), fx.Replace(cfg, logger, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: time.Second}))
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
