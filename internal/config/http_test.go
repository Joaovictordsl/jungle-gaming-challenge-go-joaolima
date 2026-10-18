package config

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestLoadHTTPDefaultsAndOverrides(t *testing.T) {
	for _, key := range []string{"HTTP_ADDR", "HTTP_SHUTDOWN_TIMEOUT"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadHTTP()
	if err != nil || cfg.Address != "127.0.0.1:8080" || cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("Configuração HTTP padrão incorreta: %+v, %v", cfg, err)
	}
	t.Setenv("HTTP_ADDR", "[::1]:0")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "750ms")
	cfg, err = LoadHTTP()
	if err != nil || cfg.Address != "[::1]:0" || cfg.ShutdownTimeout != 750*time.Millisecond {
		t.Fatalf("Configuração HTTP não aplicou valores do ambiente: %+v, %v", cfg, err)
	}
}

func TestLoadHTTPRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"endereço vazio", "HTTP_ADDR", ""},
		{"sem porta", "HTTP_ADDR", "localhost"},
		{"host com espaços", "HTTP_ADDR", "bad host:8080"},
		{"porta inválida", "HTTP_ADDR", "127.0.0.1:abc"},
		{"porta ausente", "HTTP_ADDR", "127.0.0.1:"},
		{"porta negativa", "HTTP_ADDR", "127.0.0.1:-1"},
		{"porta fora do limite", "HTTP_ADDR", "127.0.0.1:65536"},
		{"prazo vazio", "HTTP_SHUTDOWN_TIMEOUT", ""},
		{"prazo inválido", "HTTP_SHUTDOWN_TIMEOUT", "abc"},
		{"prazo zero", "HTTP_SHUTDOWN_TIMEOUT", "0s"},
		{"prazo negativo", "HTTP_SHUTDOWN_TIMEOUT", "-1s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
			t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "5s")
			t.Setenv(tc.key, tc.value)
			if _, err := LoadHTTP(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Esperava ErrInvalidConfig; recebeu %v", err)
			}
		})
	}
}
