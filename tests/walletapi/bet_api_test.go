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
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

func TestBetAPIWithRealKeycloakAndPostgres(t *testing.T) {
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
			t.Fatal("Não foi possível obter token; configure o provedor local antes do teste")
		}
		return body.AccessToken
	}
	internal := token("jungle-internal", "KEYCLOAK_INTERNAL_CLIENT_SECRET", "jungle_internal_local")
	provider := token("jungle-provider-a", "KEYCLOAK_PROVIDER_A_CLIENT_SECRET", "jungle_provider_a_local")
	var server *httpserver.Server
	var inbox *message.Service
	application := app.New(fx.Replace(database, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second},
		config.Auth{IssuerURL: issuer, Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 3 * time.Second, ProviderClients: map[string]string{"jungle-provider-a": "provider-a"}},
		slog.New(slog.NewJSONHandler(io.Discard, nil))), fx.Populate(&server, &inbox))
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
	request := func(method, path, body, key, bearer string, want int) []byte {
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
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s: HTTP %d, esperado %d; %s; %v", method, path, response.StatusCode, want, data, err)
		}
		return data
	}
	request("POST", "/wallets", `{"playerId":"bet-player","initialBalance":{"amount":"100.00","currency":"BRL"}}`, "", provider, 403)
	created := request("POST", "/wallets", `{"playerId":"bet-player","initialBalance":{"amount":"100.00","currency":"BRL"}}`, "", internal, 201)
	var w apiWallet
	if err := json.Unmarshal(created, &w); err != nil {
		t.Fatal(err)
	}
	body := func(external, amount string) string {
		data, err := json.Marshal(map[string]any{"providerId": "provider-a", "externalTransactionId": external, "walletId": w.ID, "playerId": w.PlayerID, "roundId": "round", "gameId": "game", "kind": "BET", "money": map[string]string{"amount": amount, "currency": "BRL"}})
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
	firstBody := body("external-1", "80.00")
	request("POST", "/wagering/transactions", firstBody, "key-1", "", 401)
	request("POST", "/wagering/transactions", firstBody, "key-1", internal, 403)
	first := decode(request("POST", "/wagering/transactions", firstBody, "key-1", provider, 200))
	if first.Status != "PROCESSED" || first.IdempotentReplay || first.Balance.Amount != "20.00" || first.TransactionID == "" {
		t.Fatal("Resultado da aposta incorreto")
	}
	request("GET", "/wallets/"+w.ID, "", "", provider, 403)
	// A autorização vem antes da consulta de replay: corpo não seleciona o provedor.
	request("POST", "/wagering/transactions", strings.Replace(firstBody, `"provider-a"`, `"provider-b"`, 1), "key-1", provider, 403)
	request("POST", "/wagering/transactions", body("external-1", "79.00"), "key-1", provider, 409)
	request("POST", "/wagering/transactions", firstBody, "another-key", provider, 409)
	request("POST", "/wagering/transactions", body("external-2", "10.00"), "key-2", provider, 200)
	replay := decode(request("POST", "/wagering/transactions", body("external-1", "00080.00"), "key-1", provider, 200))
	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID || replay.Balance != first.Balance {
		t.Fatal("Replay devolveu saldo atual ou reaplicou débito")
	}
	rejected := decode(request("POST", "/wagering/transactions", body("external-3", "80.00"), "key-3", provider, 422))
	if rejected.Status != "REJECTED" || rejected.FailureCode != "INSUFFICIENT_FUNDS" || rejected.Balance.Amount != "10.00" {
		t.Fatal("Rejeição não auditável")
	}
	replayRejected := decode(request("POST", "/wagering/transactions", body("external-3", "80.00"), "key-3", provider, 422))
	if !replayRejected.IdempotentReplay || replayRejected.TransactionID != rejected.TransactionID {
		t.Fatal("Rejeição não idempotente")
	}
	request("POST", "/wagering/transactions", body("zero", "0.00"), "zero-key", provider, 400)
	request("POST", "/wagering/transactions", strings.Replace(firstBody, `"BET"`, `"OPENING"`, 1), "opening-key", provider, 400)
	assertCounts := func(want [3]int) {
		t.Helper()
		var got [3]int
		err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM outbox_events)`).Scan(&got[0], &got[1], &got[2])
		if err != nil || got != want {
			t.Fatalf("Registros financeiros parciais ou duplicados: %v, esperado %v, erro %v", got, want, err)
		}
	}
	assertCounts([3]int{4, 3, 7})
	// Falha no segundo evento de uma aposta deve reverter saldo, resultado e ledger.
	_, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_bet_event CHECK
  (NOT (event_type='WalletBalanceChanged' AND payload->'data'->'money'->>'amount'='9.99'))`)
	if err != nil {
		t.Fatal(err)
	}
	request("POST", "/wagering/transactions", body("retry", "9.99"), "retry-key", provider, 500)
	assertCounts([3]int{4, 3, 7})
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_bet_event`); err != nil {
		t.Fatal(err)
	}
	retried := decode(request("POST", "/wagering/transactions", body("retry", "9.99"), "retry-key", provider, 200))
	if retried.IdempotentReplay || retried.Balance.Amount != "0.01" {
		t.Fatal("Rollback impediu retry seguro")
	}
	assertCounts([3]int{5, 4, 9})
	loaded := request("GET", "/wallets/"+w.ID, "", "", internal, 200)
	if err := json.Unmarshal(loaded, &w); err != nil || w.Balance.Amount != "0.01" || w.Version != 4 {
		t.Fatal("Saldo e versão divergiram do ledger")
	}
	// Esgotamento na mesma aplicação/inbox usada pelo consumidor. Após remover
	// a falha injetada, todos os tipos devem retornar o resultado FAILED por HTTP.
	for i, kind := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		amount := "0.01"
		if kind == "LOSS" {
			amount = "0.00"
		}
		if kind == "REFUND" || kind == "ROLLBACK" {
			amount = "80.00"
		}
		raw := strings.Replace(body("failed-"+kind, amount), `"kind":"BET"`, `"kind":"`+kind+`"`, 1)
		if kind == "REFUND" || kind == "ROLLBACK" {
			raw = strings.TrimSuffix(raw, "}") + `,"referenceExternalTransactionId":"external-1"}`
		}
		key := "failed-key-" + kind
		command, _, err := appwager.DecodeCommand([]byte(raw), key)
		if err != nil {
			t.Fatal(err)
		}
		e := message.Envelope{MessageID: "failed-msg-" + kind, OccurredAt: time.Now().UTC(), Command: command}
		if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT inject_failed_http CHECK(event_type='never') NOT VALID`); err != nil {
			t.Fatal(err)
		}
		failed, _, err := inbox.Execute(ctx, "test-consumer", e, true)
		if message.PermanentCode(err) != "attempts_exhausted" || failed.Status != "FAILED" {
			t.Fatal("falha não auditada", err)
		}
		if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT inject_failed_http`); err != nil {
			t.Fatal(err)
		}
		r := decode(request("POST", "/wagering/transactions", raw, key, provider, 500))
		if r.Status != "FAILED" || r.FailureCode != "INFRASTRUCTURE_RETRIES_EXHAUSTED" || !r.IdempotentReplay || r.TransactionID != failed.TransactionID || r.Balance.Amount != "0.01" {
			t.Fatal("HTTP reaplicou operação FAILED")
		}
		queried := decode(request("GET", "/wagering/transactions/"+r.TransactionID, "", "", provider, 200))
		if queried.TransactionID != r.TransactionID || queried.Status != r.Status || queried.FailureCode != r.FailureCode {
			t.Fatal("GET perdeu auditoria FAILED")
		}
		request("POST", "/wagering/transactions", raw, key, internal, 403)
		request("POST", "/wagering/transactions", strings.Replace(raw, `"roundId":"round"`, `"roundId":"changed"`, 1), key, provider, 409)
		assertCounts([3]int{6 + i, 4, 9})
	}
	loaded = request("GET", "/wallets/"+w.ID, "", "", internal, 200)
	if json.Unmarshal(loaded, &w) != nil || w.Balance.Amount != "0.01" || w.Version != 4 {
		t.Fatal("FAILED movimentou carteira")
	}
	observed := string(request("GET", "/metrics", "", "", internal, 200))
	for _, want := range []string{
		`jungle_wager_responses_total{transport="http",kind="BET",status="PROCESSED"} 4`,
		`jungle_wager_transactions{kind="BET",status="PROCESSED"} 3`,
		`jungle_wager_transactions{kind="BET",status="FAILED"} 1`,
		`jungle_wager_duplicates_total{transport="http"} 7`,
		`jungle_metrics_source_up{source="postgres"} 1`,
	} {
		if !strings.Contains(observed, want+"\n") {
			t.Fatal("métrica HTTP incorreta", want)
		}
	}
}
