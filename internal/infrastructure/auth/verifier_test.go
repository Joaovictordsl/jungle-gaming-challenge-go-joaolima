package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"jungle-gaming-challenge-go-joaolima/internal/config"
)

func TestVerifierValidatesAccessTokenAndBuildsPrincipal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Auth{IssuerURL: "https://idp.example/realms/jungle", Audience: "jungle-api", HTTPTimeout: time.Second}
	v := &Verifier{cfg: cfg, verifier: oidc.NewVerifier(cfg.IssuerURL,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}},
		&oidc.Config{ClientID: cfg.Audience, SupportedSigningAlgs: []string{oidc.RS256}})}
	now := time.Now().Unix()
	for _, tc := range []struct {
		name, field string
		value       any
		invalid     bool
	}{
		{name: "válido"},
		{name: "issuer incorreto", field: "iss", value: "https://other.example", invalid: true},
		{name: "audiência incorreta", field: "aud", value: "other-api", invalid: true},
		{name: "expirado", field: "exp", value: now - 10, invalid: true},
		{name: "sem expiração", field: "exp", value: nil, invalid: true},
		{name: "subject vazio", field: "sub", value: "", invalid: true},
		{name: "cliente vazio", field: "azp", value: "", invalid: true},
		{name: "ID token", field: "typ", value: "ID", invalid: true},
		{name: "not before futuro", field: "nbf", value: now + 120, invalid: true},
		{name: "emissão futura", field: "iat", value: now + 120, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"iss": cfg.IssuerURL, "aud": "jungle-api", "sub": "service-account-id", "azp": "jungle-internal",
				"typ": "Bearer", "iat": now, "exp": now + 300,
				"resource_access": map[string]any{"jungle-api": map[string]any{"roles": []string{"wallets:read"}},
					"other-api": map[string]any{"roles": []string{"wallets:write"}}}}
			if tc.field != "" {
				claims[tc.field] = tc.value
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			signed, err := signer.Sign(data)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := signed.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			principal, err := v.Verify(context.Background(), raw)
			if tc.invalid {
				if !errors.Is(err, ErrInvalidToken) || principal.Subject() != "" || principal.HasRole("wallets:read") {
					t.Fatal("Token inválido não pode produzir uma identidade autenticada")
				}
				return
			}
			if err != nil || principal.Subject() != "service-account-id" || principal.ClientID() != "jungle-internal" || !principal.HasRole("wallets:read") || principal.HasRole("wallets:write") {
				t.Fatalf("Principal deve usar apenas as claims verificadas da audiência configurada: %v", err)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(canceled, "token"); !errors.Is(err, context.Canceled) {
		t.Fatal("Verificação deve preservar cancelamento")
	}
	if _, err := (&Verifier{}).Verify(context.Background(), "token"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("Verificador não iniciado deve rejeitar chamadas")
	}
}
