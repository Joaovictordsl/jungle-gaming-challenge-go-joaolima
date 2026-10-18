//go:build oidcsimulation

package oidc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

const mockURL = "http://127.0.0.1:4900/keycloak"

func mockRequest(t *testing.T, client *http.Client, method, path string, target any) {
	t.Helper()
	request, err := http.NewRequest(method, mockURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("Inicie a simulação Postman na porta 4900 antes dos testes")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Controle da simulação retornou %d", response.StatusCode)
	}
	if target != nil && json.NewDecoder(response.Body).Decode(target) != nil {
		t.Fatal("Resposta inválida da simulação")
	}
}

func simulatedToken(t *testing.T, client *http.Client, kind string) string {
	t.Helper()
	var response struct {
		AccessToken string `json:"access_token"`
	}
	mockRequest(t, client, http.MethodGet, "/__fixtures/token?kind="+kind, &response)
	if response.AccessToken == "" {
		t.Fatal("Fixture não retornou token")
	}
	return response.AccessToken
}

type walletPermissionProbe struct{ saves atomic.Int32 }

func (p *walletPermissionProbe) SaveOpening(context.Context, wallet.Wallet) error {
	p.saves.Add(1)
	return nil
}

func (*walletPermissionProbe) NewID() (string, error) { return "permission-wallet", nil }
func (*walletPermissionProbe) Now() time.Time         { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
func (*walletPermissionProbe) Create(context.Context, wallet.Wallet) error {
	return errors.New("Create não participa da abertura HTTP")
}
func (p *walletPermissionProbe) FindByID(_ context.Context, id string) (wallet.Wallet, error) {
	if id != "permission-wallet" {
		return wallet.Wallet{}, wallet.ErrNotFound
	}
	zero, err := money.New("0.00", "BRL")
	if err != nil {
		return wallet.Wallet{}, err
	}
	return wallet.NewWallet(id, "permission-player", zero, p.Now())
}

func middlewareApplication(t *testing.T) (*fx.App, *gin.Engine, *walletPermissionProbe) {
	t.Helper()
	var router *gin.Engine
	var middleware *httpserver.Authentication
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := config.Auth{IssuerURL: mockURL + "/realms/jungle", Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 500 * time.Millisecond}
	application := fx.New(auth.Module, fx.Supply(logger), fx.Replace(cfg),
		fx.Provide(httpserver.NewRouter, httpserver.NewAuthentication), fx.Populate(&router, &middleware), fx.NopLogger)
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := application.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	router.GET("/protected", middleware.RequireInternalPermission("wallets:write"), func(c *gin.Context) {
		principal, ok := httpserver.AuthenticatedPrincipal(c)
		if !ok || principal.Subject() == "" || principal.ClientID() != "jungle-internal" {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"authorized": true})
	})
	probe := new(walletPermissionProbe)
	opener := applicationwallet.NewOpenWallet(probe, probe, probe)
	httpserver.RegisterWalletRoutes(router, middleware, httpserver.NewWalletHandler(opener, probe, logger))
	return application, router, probe
}

func walletRequest(t *testing.T, client *http.Client, address, method, token string, want int) {
	t.Helper()
	path, body := "/wallets/permission-wallet", ""
	if method == http.MethodPost {
		path, body = "/wallets", `{"playerId":"permission-player","initialBalance":{"amount":"0.00","currency":"BRL"}}`
	}
	request, err := http.NewRequest(method, address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s retornou %d, esperava %d", method, path, response.StatusCode, want)
	}
}

func protectedRequest(t *testing.T, client *http.Client, address, token string, want int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, address+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Falha na chamada à API real de teste: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("Middleware retornou %d, esperava %d", response.StatusCode, want)
	}
}

func TestMiddlewareAgainstPostmanOIDCSimulation(t *testing.T) {
	client := &http.Client{Timeout: 3 * time.Second}
	mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil)
	t.Cleanup(func() { mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil) })
	application, router, probe := middlewareApplication(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	valid := simulatedToken(t, client, "valid")
	protectedRequest(t, client, server.URL, "", http.StatusUnauthorized)
	protectedRequest(t, client, server.URL, valid, http.StatusOK)
	protectedRequest(t, client, server.URL, valid, http.StatusOK)
	var stats struct{ DiscoveryRequests, JWKSRequests, AuthorizationHeaders int }
	mockRequest(t, client, http.MethodGet, "/__fixtures/stats", &stats)
	if stats.DiscoveryRequests != 1 || stats.JWKSRequests != 1 || stats.AuthorizationHeaders != 0 {
		t.Fatalf("Discovery e JWKS devem ser consultados uma vez, sem encaminhar token: %+v", stats)
	}
	t.Run("permissões das rotas de carteira", func(t *testing.T) {
		readOnly := simulatedToken(t, client, "read-only")
		walletRequest(t, client, server.URL, http.MethodGet, readOnly, http.StatusOK)
		walletRequest(t, client, server.URL, http.MethodPost, readOnly, http.StatusForbidden)
		walletRequest(t, client, server.URL, http.MethodPost, simulatedToken(t, client, "other-client"), http.StatusForbidden)
		if probe.saves.Load() != 0 {
			t.Fatal("Uma requisição sem autorização chegou à persistência")
		}
		walletRequest(t, client, server.URL, http.MethodPost, valid, http.StatusCreated)
		if probe.saves.Load() != 1 {
			t.Fatal("A abertura autorizada deve persistir uma vez")
		}
	})
	for _, kind := range []string{"expired", "wrong-issuer", "wrong-audience", "bad-signature", "unsigned", "hmac", "missing-subject", "missing-expiry", "not-before", "future-issued", "id-token"} {
		t.Run(kind, func(t *testing.T) {
			protectedRequest(t, client, server.URL, simulatedToken(t, client, kind), http.StatusUnauthorized)
		})
	}
	for _, kind := range []string{"other-client", "read-only", "wrong-role-audience", "realm-role"} {
		t.Run(kind, func(t *testing.T) {
			protectedRequest(t, client, server.URL, simulatedToken(t, client, kind), http.StatusForbidden)
		})
	}
	response, err := client.Get(server.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("Liveness deve permanecer público")
	}

	// Uma chave já conhecida permite validação local mesmo com o JWKS fora do ar.
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure?jwks=503", nil)
	protectedRequest(t, client, server.URL, valid, http.StatusOK)
	mockRequest(t, client, http.MethodPost, "/__fixtures/rotate", nil)
	rotated := simulatedToken(t, client, "valid")
	protectedRequest(t, client, server.URL, rotated, http.StatusServiceUnavailable)
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure", nil)
	protectedRequest(t, client, server.URL, rotated, http.StatusOK)

	// Uma consulta por chave ainda não conhecida não pode bloquear indefinidamente.
	mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil)
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure?delay=1500", nil)
	started := time.Now()
	protectedRequest(t, client, server.URL, simulatedToken(t, client, "valid"), http.StatusServiceUnavailable)
	if time.Since(started) > 2*time.Second {
		t.Fatal("Timeout configurado para OIDC não limitou a chamada")
	}
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure?discovery=503", nil)
	failedApplication, _, _ := middlewareApplication(t)
	if err := failedApplication.Start(ctx); !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("Discovery indisponível deve impedir inicialização: %v", err)
	}
}
