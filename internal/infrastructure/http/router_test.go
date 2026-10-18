package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLivenessContract(t *testing.T) {
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(nethttp.MethodGet, "/health/live", nil))
	if response.Code != nethttp.StatusOK || response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("Liveness deve responder 200 com status ok; recebeu %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("Liveness deve retornar JSON sem cache")
	}
}

func TestRouterRejectsUnsupportedMethodsAndUnknownRoutes(t *testing.T) {
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{nethttp.MethodPost, "/health/live", `{"error":"method_not_allowed"}`, nethttp.StatusMethodNotAllowed},
		{nethttp.MethodGet, "/missing", `{"error":"not_found"}`, nethttp.StatusNotFound},
		{nethttp.MethodGet, "/health/live/", `{"error":"not_found"}`, nethttp.StatusNotFound},
		{nethttp.MethodGet, "/health/ready", `{"error":"not_found"}`, nethttp.StatusNotFound},
		{nethttp.MethodPost, "/wallets", `{"error":"not_found"}`, nethttp.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.status || response.Body.String() != tc.body {
			t.Fatalf("%s %s: recebeu %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
		if tc.status == nethttp.StatusMethodNotAllowed && response.Header().Get("Allow") != nethttp.MethodGet {
			t.Fatal("405 deve informar o método permitido")
		}
	}
}

func TestRecoveryReturnsJSONWithoutExposingRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	router := NewRouter(slog.New(slog.NewJSONHandler(&logs, nil)))
	router.POST("/test-panic", func(*gin.Context) { panic("private-panic-value") })
	request := httptest.NewRequest(nethttp.MethodPost, "/test-panic?token=private-query", strings.NewReader("private-body"))
	request.Header.Set("Authorization", "Bearer private-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != nethttp.StatusInternalServerError || response.Body.String() != `{"error":"internal_error"}` {
		t.Fatalf("Recovery deve produzir erro JSON genérico: %d %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"private-token", "private-query", "private-body", "private-panic-value"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(response.Body.String(), secret) {
			t.Fatalf("Resposta ou logs expuseram %s", secret)
		}
	}
	decoder := json.NewDecoder(&logs)
	count := 0
	for decoder.More() {
		var record struct {
			Message string `json:"msg"`
		}
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("Logs HTTP devem ser JSON: %v", err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("Esperava log de erro e log de acesso; recebeu %d", count)
	}
}
