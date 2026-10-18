package http

import (
	"context"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
)

type tokenVerifierFunc func(context.Context, string) (auth.Principal, error)

func (f tokenVerifierFunc) Verify(ctx context.Context, raw string) (auth.Principal, error) {
	return f(ctx, raw)
}

func TestAuthenticationRejectsMissingOrAmbiguousCredentials(t *testing.T) {
	for _, headers := range [][]string{nil, {""}, {"Basic credentials"}, {"Bearer"}, {"Bearer one two"}, {"Bearer one", "Bearer two"}, {"Bearer " + strings.Repeat("x", 17<<10)}} {
		router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
		middleware := NewAuthentication(tokenVerifierFunc(func(context.Context, string) (auth.Principal, error) {
			t.Fatal("Credencial ausente ou ambígua não deve chamar o verificador")
			return auth.Principal{}, nil
		}), config.Auth{Audience: "jungle-api", InternalClientID: "jungle-internal"})
		called := false
		router.GET("/protected", middleware.RequireInternalPermission("wallets:read"), func(*gin.Context) { called = true })
		request := httptest.NewRequest(nethttp.MethodGet, "/protected?access_token=private-query-token", nil)
		request.AddCookie(&nethttp.Cookie{Name: "access_token", Value: "private-cookie-token"})
		for _, header := range headers {
			request.Header.Add("Authorization", header)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != nethttp.StatusUnauthorized || response.Body.String() != `{"error":"unauthorized"}` || called {
			t.Fatalf("Credenciais devem vir somente de um Bearer header válido: %d %s", response.Code, response.Body.String())
		}
		if response.Header().Get("WWW-Authenticate") != `Bearer realm="jungle-api"` {
			t.Fatal("401 deve incluir o desafio Bearer")
		}
	}
}

func TestAuthenticationMapsVerificationFailuresWithoutCallingHandler(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		status int
		body   string
	}{
		{"token inválido", auth.ErrInvalidToken, nethttp.StatusUnauthorized, `{"error":"unauthorized"}`},
		{"IdP indisponível", auth.ErrUnavailable, nethttp.StatusServiceUnavailable, `{"error":"authentication_unavailable"}`},
		{"cancelamento", context.Canceled, nethttp.StatusServiceUnavailable, `{"error":"authentication_unavailable"}`},
		{"timeout", context.DeadlineExceeded, nethttp.StatusServiceUnavailable, `{"error":"authentication_unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			middleware := NewAuthentication(tokenVerifierFunc(func(got context.Context, raw string) (auth.Principal, error) {
				if got != ctx || raw != "private-token" {
					t.Fatal("Verificador deve receber o contexto e o token da chamada")
				}
				calls++
				return auth.Principal{}, tc.cause
			}), config.Auth{Audience: "jungle-api"})
			router.GET("/protected", middleware.Authenticate(), func(*gin.Context) { t.Fatal("Handler protegido foi executado") })
			request := httptest.NewRequest(nethttp.MethodGet, "/protected", nil).WithContext(ctx)
			request.Header.Set("Authorization", "bEaReR private-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || response.Body.String() != tc.body || calls != 1 {
				t.Fatalf("Falha deve interromper a requisição com erro genérico: %d %s", response.Code, response.Body.String())
			}
			if tc.status == nethttp.StatusUnauthorized && !strings.Contains(response.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
				t.Fatal("Token inválido deve produzir desafio Bearer apropriado")
			}
		})
	}
}

func TestAuthenticationRejectsUninitializedPrincipal(t *testing.T) {
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	middleware := NewAuthentication(tokenVerifierFunc(func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{}, nil
	}), config.Auth{Audience: "jungle-api"})
	router.GET("/protected", middleware.Authenticate(), func(*gin.Context) { t.Fatal("Identidade vazia não deve executar o handler") })
	request := httptest.NewRequest(nethttp.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != nethttp.StatusUnauthorized {
		t.Fatal("Identidade vazia deve ser rejeitada mesmo se um adapter não retornar erro")
	}
}
