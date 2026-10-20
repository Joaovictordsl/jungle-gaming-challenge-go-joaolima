package config

import (
	"errors"
	"testing"
)

func TestOutboxConfig(t *testing.T) {
	t.Setenv("OUTBOX_ENABLED", "true")
	t.Setenv("OUTBOX_ACCESS_KEY_ID", "local-key")
	t.Setenv("OUTBOX_SECRET_ACCESS_KEY", "local-secret")
	c, err := LoadOutbox()
	if err != nil || !c.Enabled || c.QueueName != "wallet-events.fifo" || c.Workers != 2 {
		t.Fatal("configuração incorreta", err)
	}
	c.SecretAccessKey = ""
	if !errors.Is(c.Validate(), ErrInvalidConfig) {
		t.Fatal("chave ausente aceita")
	}
	if err := (Outbox{}).Validate(); err != nil {
		t.Fatal("publisher desabilitado requer credenciais")
	}
	t.Setenv("OUTBOX_WORKERS", "17")
	if _, err := LoadOutbox(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("workers inválidos aceitos")
	}
	t.Setenv("OUTBOX_ENABLED", "invalid")
	if _, err := LoadOutbox(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("booleano inválido aceito")
	}
}
