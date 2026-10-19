//go:build integration && keycloak

package walletapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/app"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

func TestRefundAPIAndReferenceWorkerWithRealKeycloakAndPostgres(t *testing.T) {
	ctx, database, pool := walletAPIDatabase(t)
	issuer := os.Getenv("TEST_KEYCLOAK_ISSUER_URL")
	if issuer == "" {
		issuer = "http://localhost:8081/realms/jungle"
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 6 * time.Second, Transport: transport}
	token := func(clientID, secretEnv, fallback string) string {
		t.Helper()
		secret := os.Getenv(secretEnv)
		if secret == "" {
			secret = fallback
		}
		response, err := client.PostForm(issuer+"/protocol/openid-connect/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}})
		if err != nil {
			t.Fatal("Keycloak inacessível")
		}
		defer response.Body.Close()
		var body struct {
			AccessToken string `json:"access_token"`
		}
		if json.NewDecoder(response.Body).Decode(&body) != nil || response.StatusCode != 200 || body.AccessToken == "" {
			t.Fatal("Não foi possível obter token do cliente de teste")
		}
		return body.AccessToken
	}
	internal := token("jungle-internal", "KEYCLOAK_INTERNAL_CLIENT_SECRET", "jungle_internal_local")
	provider := token("jungle-provider-a", "KEYCLOAK_PROVIDER_A_CLIENT_SECRET", "jungle_provider_a_local")
	var server *httpserver.Server
	application := app.New(fx.Replace(database, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second},
		config.Auth{IssuerURL: issuer, Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 3 * time.Second, ProviderClients: map[string]string{"jungle-provider-a": "provider-a"}},
		slog.New(slog.NewJSONHandler(io.Discard, nil))), fx.Populate(&server))
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := application.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, key, bearer string) ([]byte, int) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, "http://"+server.Address()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return data, response.StatusCode
	}
	assertRequest := func(method, path, body, key, bearer string, want int) []byte {
		t.Helper()
		data, got := request(method, path, body, key, bearer)
		if got != want {
			t.Fatalf("%s %s: HTTP %d, esperado %d: %s", method, path, got, want, data)
		}
		return data
	}
	created := assertRequest("POST", "/wallets", `{"playerId":"refund-player","initialBalance":{"amount":"100.00","currency":"BRL"}}`, "", internal, 201)
	var w apiWallet
	if err := json.Unmarshal(created, &w); err != nil {
		t.Fatal(err)
	}
	body := func(kind, external, amount, ref string) string {
		fields := map[string]any{"providerId": "provider-a", "externalTransactionId": external, "walletId": w.ID, "playerId": w.PlayerID, "roundId": "round", "gameId": "game", "kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"}}
		if ref != "" {
			fields["referenceExternalTransactionId"] = ref
		}
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	type result struct {
		TransactionID, Status, FailureCode string
		Balance                            struct{ Amount, Currency string }
		IdempotentReplay                   bool
	}
	decode := func(data []byte) result {
		t.Helper()
		var r result
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	refund := body("REFUND", "refund-1", "80.00", "bet-1")
	assertRequest("POST", "/wagering/transactions", refund, "refund-key", "", 401)
	assertRequest("POST", "/wagering/transactions", refund, "refund-key", internal, 403)
	assertRequest("POST", "/wagering/transactions", strings.Replace(refund, `"provider-a"`, `"provider-b"`, 1), "refund-key", provider, 403)
	assertRequest("POST", "/wagering/transactions", body("REFUND", "invalid", "80.00", ""), "invalid-key", provider, 400)
	assertRequest("POST", "/wagering/transactions", body("REFUND", "zero", "0.00", "bet-1"), "zero-key", provider, 400)
	assertRequest("POST", "/wagering/transactions", body("BET", "bet-1", "80.00", ""), "bet-key", provider, 200)
	partial := decode(assertRequest("POST", "/wagering/transactions", body("REFUND", "partial", "79.00", "bet-1"), "partial-key", provider, 422))
	if partial.FailureCode != "REFERENCE_AMOUNT_MISMATCH" || partial.Balance.Amount != "20.00" {
		t.Fatal("Devolução parcial movimentou saldo")
	}
	first := decode(assertRequest("POST", "/wagering/transactions", refund, "refund-key", provider, 200))
	if first.Status != "PROCESSED" || first.Balance.Amount != "100.00" || first.IdempotentReplay {
		t.Fatal("REFUND não devolveu exatamente")
	}
	duplicate := decode(assertRequest("POST", "/wagering/transactions", body("REFUND", "duplicate", "80.00", "bet-1"), "duplicate-key", provider, 422))
	if duplicate.FailureCode != "ALREADY_REVERSED" {
		t.Fatal("Devolução duplicada não rejeitada")
	}
	assertRequest("POST", "/wagering/transactions", body("REFUND", "refund-1", "79.00", "bet-1"), "refund-key", provider, 409)
	assertRequest("POST", "/wagering/transactions", refund, "other-key", provider, 409)
	assertRequest("POST", "/wagering/transactions", body("BET", "bet-2", "10.00", ""), "bet-2-key", provider, 200)
	repeated := decode(assertRequest("POST", "/wagering/transactions", body("REFUND", "refund-1", "00080.00", "bet-1"), "refund-key", provider, 200))
	if !repeated.IdempotentReplay || repeated.TransactionID != first.TransactionID || repeated.Balance != first.Balance {
		t.Fatal("Replay retornou saldo atual")
	}
	pendingBody := body("REFUND", "pending-refund", "5.00", "bet-later")
	pending := decode(assertRequest("POST", "/wagering/transactions", pendingBody, "pending-key", provider, 202))
	if pending.Status != "PENDING_REFERENCE" || pending.Balance.Amount != "90.00" {
		t.Fatal("Ausência da BET não persistiu espera")
	}
	var entries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id=$1`, pending.TransactionID).Scan(&entries); err != nil || entries != 0 {
		t.Fatal("Pendência creditou saldo", err)
	}
	assertRequest("POST", "/wagering/transactions", body("BET", "bet-later", "5.00", ""), "later-key", provider, 200)
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, status := request("POST", "/wagering/transactions", pendingBody, "pending-key", provider)
		if status == 200 {
			completed := decode(data)
			if completed.TransactionID != pending.TransactionID || !completed.IdempotentReplay || completed.Balance.Amount != "90.00" {
				t.Fatal("Worker não confirmou a devolução original")
			}
			break
		}
		if status != 202 || time.Now().After(deadline) {
			t.Fatalf("Worker não retomou REFUND: HTTP %d %s", status, data)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	var counts [3]int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM outbox_events)`).Scan(&counts[0], &counts[1], &counts[2]); err != nil || counts != [3]int{8, 6, 15} {
		t.Fatalf("Efeitos parciais ou duplicados: %v %v", counts, err)
	}
	loaded := assertRequest("GET", "/wallets/"+w.ID, "", "", internal, 200)
	if err := json.Unmarshal(loaded, &w); err != nil || w.Balance.Amount != "90.00" || w.Version != 6 {
		t.Fatal("Saldo final incorreto", err)
	}
}
