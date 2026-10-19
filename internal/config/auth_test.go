package config

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestAuthConfigurationDefaultsAndOverrides(t *testing.T) {
	for _, key := range []string{"OIDC_ISSUER_URL", "OIDC_AUDIENCE", "OIDC_INTERNAL_CLIENT_ID", "OIDC_HTTP_TIMEOUT", "OIDC_PROVIDER_CLIENTS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadAuth()
	if err != nil || cfg.IssuerURL != "http://localhost:8081/realms/jungle" || cfg.Audience != "jungle-api" || cfg.InternalClientID != "jungle-internal" || cfg.HTTPTimeout != 3*time.Second {
		t.Fatalf("Valores OIDC padrão inválidos: %v", err)
	}
	t.Setenv("OIDC_ISSUER_URL", "https://idp.example/realms/jungle")
	t.Setenv("OIDC_AUDIENCE", "other-api")
	t.Setenv("OIDC_INTERNAL_CLIENT_ID", "other-internal")
	t.Setenv("OIDC_HTTP_TIMEOUT", "500ms")
	cfg, err = LoadAuth()
	if err != nil || cfg.IssuerURL != "https://idp.example/realms/jungle" || cfg.Audience != "other-api" || cfg.InternalClientID != "other-internal" || cfg.HTTPTimeout != 500*time.Millisecond {
		t.Fatalf("Variáveis OIDC não foram aplicadas: %v", err)
	}
}

func TestAuthConfigurationRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"OIDC_ISSUER_URL", ""}, {"OIDC_ISSUER_URL", "http://idp.example/realm"},
		{"OIDC_ISSUER_URL", "http://localhost:8081/realm/"}, {"OIDC_ISSUER_URL", "https://user:secret@idp.example/realm"},
		{"OIDC_ISSUER_URL", "https://idp.example/realm?token=secret"}, {"OIDC_ISSUER_URL", "https://idp.example/realm#fragment"},
		{"OIDC_AUDIENCE", ""}, {"OIDC_AUDIENCE", "bad audience"}, {"OIDC_AUDIENCE", "bad\"audience"},
		{"OIDC_INTERNAL_CLIENT_ID", ""}, {"OIDC_HTTP_TIMEOUT", "0s"}, {"OIDC_HTTP_TIMEOUT", "-1s"}, {"OIDC_HTTP_TIMEOUT", "invalid"},
		{"OIDC_PROVIDER_CLIENTS", `null`}, {"OIDC_PROVIDER_CLIENTS", `[]`}, {"OIDC_PROVIDER_CLIENTS", `{"jungle-internal":"provider-a"}`},
		{"OIDC_PROVIDER_CLIENTS", `{"":"provider-a"}`}, {"OIDC_PROVIDER_CLIENTS", `{"client":" "}`},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle")
			t.Setenv("OIDC_AUDIENCE", "jungle-api")
			t.Setenv("OIDC_INTERNAL_CLIENT_ID", "jungle-internal")
			t.Setenv("OIDC_HTTP_TIMEOUT", "3s")
			t.Setenv(tc.key, tc.value)
			if _, err := LoadAuth(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Esperava ErrInvalidConfig: %v", err)
			}
		})
	}
}
