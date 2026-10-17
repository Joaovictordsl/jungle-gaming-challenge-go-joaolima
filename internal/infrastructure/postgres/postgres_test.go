package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/config"
)

type recordingLifecycle struct {
	hooks []fx.Hook
}

func (l *recordingLifecycle) Append(hook fx.Hook) {
	l.hooks = append(l.hooks, hook)
}

func TestInvalidDatabaseURLDoesNotExposeCredentials(t *testing.T) {
	const password = "password-must-not-appear"
	cfg := config.Config{
		DatabaseURL:            "postgres://postgres:" + password + "@localhost:5433/jungle?sslmode=invalid",
		DatabaseConnectTimeout: time.Second,
		DatabaseMaxConns:       1,
	}
	lifecycle := &recordingLifecycle{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pool, err := NewPool(lifecycle, cfg, logger)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Esperava ErrInvalidConfig; recebeu %v", err)
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), cfg.DatabaseURL) {
		t.Fatal("Erro expôs credenciais")
	}
	if pool != nil || len(lifecycle.hooks) != 0 {
		t.Fatal("Configuração inválida não deve criar pool nem registrar hooks")
	}
}

func TestConnectionErrorPreservesCancellationWithoutSecrets(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := connectionError(cause)
		if !errors.Is(err, ErrUnavailable) || !errors.Is(err, cause) {
			t.Fatalf("Erro deve preservar indisponibilidade e cancelamento: %v", err)
		}
	}
	const secret = "sensitive-driver-detail"
	err := connectionError(errors.New(secret))
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), secret) {
		t.Fatal("Erro deve classificar a falha sem propagar detalhes sensíveis")
	}
}
