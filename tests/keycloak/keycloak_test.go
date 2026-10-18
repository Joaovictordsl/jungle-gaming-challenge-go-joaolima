//go:build keycloak

package keycloak_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

// Estes testes verificam a configuração do IdP real, separadamente do middleware
// da API. Tokens e secrets nunca são impressos nos resultados dos testes.
type discovery struct {
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       string `json:"error"`
}

func keycloakEnvironment(t *testing.T) (context.Context, *http.Client, discovery, string) {
	t.Helper()
	issuer := os.Getenv("TEST_KEYCLOAK_ISSUER_URL")
	if issuer == "" {
		issuer = "http://localhost:8081/realms/jungle"
	}
	issuer = strings.TrimSuffix(issuer, "/")
	secret := os.Getenv("KEYCLOAK_INTERNAL_CLIENT_SECRET")
	if secret == "" {
		secret = "jungle_internal_local"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		t.Fatal("TEST_KEYCLOAK_ISSUER_URL inválida")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("Keycloak inacessível; execute docker compose up -d --wait keycloak")
	}
	defer response.Body.Close()
	var config discovery
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&config) != nil {
		t.Fatal("Discovery OIDC não retornou configuração válida")
	}
	if config.Issuer != issuer || config.TokenEndpoint != issuer+"/protocol/openid-connect/token" || config.JWKSURI != issuer+"/protocol/openid-connect/certs" {
		t.Fatal("Issuer ou endpoints publicados não correspondem à configuração local")
	}
	return ctx, client, config, secret
}

func requestToken(t *testing.T, ctx context.Context, client *http.Client, endpoint, secret, grant string) (int, tokenResponse) {
	t.Helper()
	form := url.Values{"grant_type": {grant}, "client_id": {"jungle-internal"}, "client_secret": {secret}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal("Endpoint de token inválido")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("Falha ao chamar o endpoint de token")
	}
	defer response.Body.Close()
	var result tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		t.Fatal("Endpoint de token não retornou JSON válido")
	}
	return response.StatusCode, result
}

func TestClientCredentialsIssuesSignedTokenWithWalletPermissions(t *testing.T) {
	ctx, client, config, secret := keycloakEnvironment(t)
	status, response := requestToken(t, ctx, client, config.TokenEndpoint, secret, "client_credentials")
	if status != http.StatusOK || !strings.EqualFold(response.TokenType, "Bearer") || response.ExpiresIn <= 0 || response.ExpiresIn > 300 {
		t.Fatalf("Client credentials deve emitir token Bearer de até 5 minutos; status=%d, erro=%s", status, response.Error)
	}
	parts := strings.Split(response.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatal("Access token deve ser um JWT assinado")
	}
	var header struct{ Alg, Kid string }
	decodeJWTPart(t, parts[0], &header)
	if header.Alg != "RS256" || header.Kid == "" {
		t.Fatal("JWT deve indicar algoritmo RS256 e chave de assinatura")
	}
	verifyTokenSignature(t, ctx, client, config.JWKSURI, header.Kid, parts)

	var claims struct {
		Iss, Sub, Azp  string
		Aud            json.RawMessage
		Iat, Exp       int64
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	decodeJWTPart(t, parts[1], &claims)
	var audiences []string
	if err := json.Unmarshal(claims.Aud, &audiences); err != nil {
		var audience string
		if err := json.Unmarshal(claims.Aud, &audience); err != nil {
			t.Fatal("JWT não contém audiência válida")
		}
		audiences = []string{audience}
	}
	if claims.Iss != config.Issuer || claims.Azp != "jungle-internal" || claims.Sub == "" || !slices.Contains(audiences, "jungle-api") {
		t.Fatal("Token deve identificar o serviço interno, o issuer local e a audiência jungle-api")
	}
	now := time.Now().Unix()
	if claims.Iat > now+10 || claims.Exp <= now || claims.Exp <= claims.Iat || claims.Exp-claims.Iat > 300 {
		t.Fatal("Instantes ou validade do token estão incorretos")
	}
	roles := claims.ResourceAccess["jungle-api"].Roles
	if len(roles) != 2 || !slices.Contains(roles, "wallets:read") || !slices.Contains(roles, "wallets:write") {
		t.Fatal("Token do serviço interno deve conter somente as duas permissões de carteira")
	}
}

func TestClientCredentialsRejectsInvalidSecret(t *testing.T) {
	ctx, client, config, secret := keycloakEnvironment(t)
	status, result := requestToken(t, ctx, client, config.TokenEndpoint, secret+"-invalid", "client_credentials")
	// Versões do IdP podem classificar a falha como invalid_client ou
	// unauthorized_client. Ambas devem rejeitar a autenticação sem emitir token.
	credentialError := result.Error == "invalid_client" || result.Error == "unauthorized_client"
	if status != http.StatusUnauthorized || !credentialError || result.AccessToken != "" {
		t.Fatalf("Secret incorreto não deve emitir token; status=%d, erro=%s", status, result.Error)
	}
}

func TestInternalClientRejectsPasswordGrant(t *testing.T) {
	ctx, client, config, secret := keycloakEnvironment(t)
	status, result := requestToken(t, ctx, client, config.TokenEndpoint, secret, "password")
	if status != http.StatusBadRequest || result.Error != "unauthorized_client" || result.AccessToken != "" {
		t.Fatalf("Cliente interno deve impedir login por senha de usuário; status=%d, erro=%s", status, result.Error)
	}
}

func TestMiddlewareVerifiesTokenFromRealKeycloak(t *testing.T) {
	ctx, client, discovery, secret := keycloakEnvironment(t)
	status, token := requestToken(t, ctx, client, discovery.TokenEndpoint, secret, "client_credentials")
	if status != http.StatusOK {
		t.Fatal("Keycloak não emitiu o token para testar o middleware")
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var middleware *httpserver.Authentication
	var router *gin.Engine
	cfg := config.Auth{IssuerURL: discovery.Issuer, Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 3 * time.Second}
	application := fx.New(auth.Module, fx.Supply(logger), fx.Replace(cfg),
		fx.Provide(httpserver.NewRouter, httpserver.NewAuthentication), fx.Populate(&middleware, &router), fx.NopLogger)
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	})
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	handler := func(c *gin.Context) {
		principal, ok := httpserver.AuthenticatedPrincipal(c)
		if !ok || principal.Subject() == "" || principal.ClientID() != "jungle-internal" {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"authorized": true})
	}
	router.GET("/protected", middleware.RequireInternalPermission("wallets:write"), handler)
	router.GET("/forbidden", middleware.RequireInternalPermission("wallets:admin"), handler)
	server := httptest.NewServer(router)
	defer server.Close()
	for _, tc := range []struct {
		path          string
		authenticated bool
		want          int
	}{
		{"/health/live", false, http.StatusOK}, {"/protected", false, http.StatusUnauthorized},
		{"/protected", true, http.StatusOK}, {"/forbidden", true, http.StatusForbidden},
	} {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+tc.path, nil)
		if tc.authenticated {
			request.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("Não foi possível testar a rota protegida")
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("Middleware com Keycloak real retornou %d, esperava %d", response.StatusCode, tc.want)
		}
	}
}

func decodeJWTPart(t *testing.T, encoded string, target any) {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(data, target) != nil {
		t.Fatal("Parte do JWT não tem codificação JSON válida")
	}
}

func verifyTokenSignature(t *testing.T, ctx context.Context, client *http.Client, jwksURL, keyID string, parts []string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		t.Fatal("URL de JWKS inválida")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("JWKS indisponível")
	}
	defer response.Body.Close()
	var jwks struct {
		Keys []struct{ Kid, Kty, Alg, Use, N, E string } `json:"keys"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&jwks) != nil {
		t.Fatal("JWKS não retornou chaves válidas")
	}
	for _, key := range jwks.Keys {
		if key.Kid != keyID || key.Kty != "RSA" || key.Alg != "RS256" || key.Use != "sig" {
			continue
		}
		modulus, modulusErr := base64.RawURLEncoding.DecodeString(key.N)
		exponent, exponentErr := base64.RawURLEncoding.DecodeString(key.E)
		signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[2])
		if modulusErr != nil || exponentErr != nil || signatureErr != nil || len(exponent) == 0 || len(exponent) > 4 {
			t.Fatal("Codificação da chave ou da assinatura inválida")
		}
		var e int
		for _, b := range exponent {
			e = e<<8 | int(b)
		}
		publicKey := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if publicKey.N.BitLen() < 2048 || rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature) != nil {
			t.Fatal("Assinatura do token não corresponde à chave pública do realm")
		}
		return
	}
	t.Fatal("Chave de assinatura do token não foi publicada no JWKS")
}
