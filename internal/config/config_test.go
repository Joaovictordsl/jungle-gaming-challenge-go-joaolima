package config

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5433/jungle?sslmode=disable")
	for _, key := range []string{"DATABASE_CONNECT_TIMEOUT", "DATABASE_MAX_CONNS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != os.Getenv("DATABASE_URL") || cfg.DatabaseConnectTimeout != 5*time.Second || cfg.DatabaseMaxConns != 10 {
		t.Fatal("Configuração não preservou a URL ou os valores padrão")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5433/jungle?sslmode=disable")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "750ms")
	t.Setenv("DATABASE_MAX_CONNS", "3")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseConnectTimeout != 750*time.Millisecond || cfg.DatabaseMaxConns != 3 {
		t.Fatal("Configuração não aplicou os valores do ambiente")
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"URL ausente", "DATABASE_URL", ""},
		{"URL com espaços", "DATABASE_URL", " \t\n"},
		{"timeout vazio", "DATABASE_CONNECT_TIMEOUT", ""},
		{"timeout inválido", "DATABASE_CONNECT_TIMEOUT", "abc"},
		{"timeout zero", "DATABASE_CONNECT_TIMEOUT", "0s"},
		{"timeout negativo", "DATABASE_CONNECT_TIMEOUT", "-1s"},
		{"conexões vazias", "DATABASE_MAX_CONNS", ""},
		{"conexões inválidas", "DATABASE_MAX_CONNS", "abc"},
		{"conexões zero", "DATABASE_MAX_CONNS", "0"},
		{"conexões negativas", "DATABASE_MAX_CONNS", "-1"},
		{"conexões fracionárias", "DATABASE_MAX_CONNS", "1.5"},
		{"conexões fora de int32", "DATABASE_MAX_CONNS", "2147483648"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost:5433/jungle?sslmode=disable")
			t.Setenv("DATABASE_CONNECT_TIMEOUT", "5s")
			t.Setenv("DATABASE_MAX_CONNS", "10")
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Esperava ErrInvalidConfig; recebeu %v", err)
			}
		})
	}
}
