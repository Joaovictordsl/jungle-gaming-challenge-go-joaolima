package http

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/health"
)

func TestReadinessPublicContractAndFailurePrivacy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure bool
		want    int
		body    string
	}{
		{"ready", false, 200, `{"status":"ready","checks":{"postgres":"ok","sqsConsumer":"disabled","sqsPublisher":"disabled"}}`},
		{"unavailable", true, 503, `{"status":"not_ready","checks":{"postgres":"unavailable","sqsConsumer":"disabled","sqsPublisher":"disabled"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			router := NewRouter(logger)
			probe := func(context.Context) error {
				if tc.failure {
					return errors.New("postgres://private-user:private-password@private-host")
				}
				return nil
			}
			RegisterReadinessRoutes(router, NewReadinessHandler(health.NewService(probe, nil, nil)))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "/health/ready", nil))
			if response.Code != tc.want || response.Body.String() != tc.body {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatal("cabeçalhos incorretos")
			}
			for _, secret := range []string{"private-user", "private-password", "private-host"} {
				if strings.Contains(logs.String()+response.Body.String(), secret) {
					t.Fatal("diagnóstico público expôs informações privadas")
				}
			}
			// Mesmo com PostgreSQL indisponível, liveness continua respondendo.
			live := httptest.NewRecorder()
			router.ServeHTTP(live, httptest.NewRequest("GET", "/health/live", nil))
			if live.Code != http.StatusOK || live.Body.String() != `{"status":"ok"}` {
				t.Fatal("liveness depende de readiness")
			}
			wrong := httptest.NewRecorder()
			router.ServeHTTP(wrong, httptest.NewRequest("POST", "/health/ready", nil))
			if wrong.Code != 405 || wrong.Header().Get("Allow") != "GET" {
				t.Fatal("método inválido aceito")
			}
		})
	}
}
