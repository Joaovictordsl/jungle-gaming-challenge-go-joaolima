package app

import (
	"errors"
	"testing"

	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/config"
)

func TestAppRejectsInvalidConfiguration(t *testing.T) {
	application := New(fx.Replace(config.Config{}, config.SQS{}), fx.NopLogger)
	if !errors.Is(application.Err(), config.ErrInvalidConfig) {
		t.Fatalf("Esperava rejeição da configuração antes de conectar; recebeu %v", application.Err())
	}
}
