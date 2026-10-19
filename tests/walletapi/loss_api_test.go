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

func TestLossAPIWithRealKeycloakAndPostgres(t *testing.T) {
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
	created := assertRequest("POST", "/wallets", `{"playerId":"loss-player","initialBalance":{"amount":"100.00","currency":"BRL"}}`, "", internal, 201)
	var w apiWallet
	if err := json.Unmarshal(created, &w); err != nil {
		t.Fatal(err)
	}
	var initialCreated, initialUpdated time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at,updated_at FROM wallets WHERE id=$1`, w.ID).Scan(&initialCreated, &initialUpdated); err != nil {
		t.Fatal(err)
	}
	body := func(kind, external, amount, currency string) string {
		fields := map[string]any{"providerId": "provider-a", "externalTransactionId": external, "walletId": w.ID, "playerId": w.PlayerID, "roundId": "round", "gameId": "game", "kind": kind, "money": map[string]string{"amount": amount, "currency": currency}}
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	type result struct {
		TransactionID, Status string
		Balance               struct{ Amount, Currency string }
		IdempotentReplay      bool
	}
	decode := func(data []byte) result {
		t.Helper()
		var r result
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	loss := body("LOSS", "loss-1", "0.00", "BRL")
	assertRequest("POST", "/wagering/transactions", loss, "loss-key", "", 401)
	assertRequest("POST", "/wagering/transactions", loss, "loss-key", "invalid-token", 401)
	assertRequest("POST", "/wagering/transactions", loss, "loss-key", internal, 403)
	assertRequest("POST", "/wagering/transactions", strings.Replace(loss, `"provider-a"`, `"provider-b"`, 1), "loss-key", provider, 403)
	first := decode(assertRequest("POST", "/wagering/transactions", loss, "loss-key", provider, 200))
	if first.Status != "PROCESSED" || first.Balance.Amount != "100.00" || first.IdempotentReplay {
		t.Fatal("Resultado de LOSS incorreto")
	}
	var afterCreated, afterUpdated time.Time
	var version int64
	if err := pool.QueryRow(ctx, `SELECT created_at,updated_at,version FROM wallets WHERE id=$1`, w.ID).Scan(&afterCreated, &afterUpdated, &version); err != nil || !afterCreated.Equal(initialCreated) || !afterUpdated.Equal(initialUpdated) || version != 1 {
		t.Fatal("LOSS alterou versão ou timestamps", err)
	}
	repeated := decode(assertRequest("POST", "/wagering/transactions", body("LOSS", "loss-1", "0000.00", "BRL"), "loss-key", provider, 200))
	if repeated.TransactionID != first.TransactionID || !repeated.IdempotentReplay {
		t.Fatal("Zero equivalente não reconhecido como replay")
	}
	assertRequest("POST", "/wagering/transactions", body("LOSS", "loss-1", "0.00", "BRL"), "another-key", provider, 409)
	assertRequest("POST", "/wagering/transactions", strings.Replace(loss, `"round"`, `"other-round"`, 1), "loss-key", provider, 409)
	assertRequest("POST", "/wagering/transactions", body("WIN", "loss-1", "1.00", "BRL"), "loss-key", provider, 409)
	assertRequest("POST", "/wagering/transactions", body("BET", "loss-1", "1.00", "BRL"), "loss-key", provider, 409)
	assertRequest("POST", "/wagering/transactions", body("LOSS", "invalid-amount", "0.01", "BRL"), "invalid-key", provider, 400)
	assertRequest("POST", "/wagering/transactions", body("LOSS", "invalid-currency", "0.00", "USD"), "currency-key", provider, 400)
	assertRequest("POST", "/wagering/transactions", strings.Replace(body("LOSS", "invalid-player", "0.00", "BRL"), `"loss-player"`, `"other-player"`, 1), "player-key", provider, 400)
	assertRequest("POST", "/wagering/transactions", strings.Replace(body("LOSS", "missing-wallet-operation", "0.00", "BRL"), w.ID, "missing-wallet", 1), "missing-key", provider, 404)
	assertRequest("POST", "/wagering/transactions", body("BET", "bet-after", "80.00", "BRL"), "bet-key", provider, 200)
	repeated = decode(assertRequest("POST", "/wagering/transactions", loss, "loss-key", provider, 200))
	if !repeated.IdempotentReplay || repeated.Balance != first.Balance || repeated.TransactionID != first.TransactionID {
		t.Fatal("Replay de LOSS retornou saldo atual")
	}
	for _, external := range []string{"loss-2", "loss-3"} {
		current := decode(assertRequest("POST", "/wagering/transactions", body("LOSS", external, "0.00", "BRL"), external+"-key", provider, 200))
		if current.Balance.Amount != "20.00" || current.IdempotentReplay {
			t.Fatal("LOSS distinto não preservou saldo")
		}
	}
	assertCounts := func(want [3]int) {
		t.Helper()
		var got [3]int
		err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM outbox_events)`).Scan(&got[0], &got[1], &got[2])
		if err != nil || got != want {
			t.Fatalf("Registros parciais ou duplicados: %v, esperado %v, erro %v", got, want, err)
		}
	}
	assertCounts([3]int{5, 2, 7})
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_loss_event CHECK (payload->'data'->>'externalTransactionId' <> 'rollback-loss')`); err != nil {
		t.Fatal(err)
	}
	rollbackBody := body("LOSS", "rollback-loss", "0.00", "BRL")
	assertRequest("POST", "/wagering/transactions", rollbackBody, "rollback-key", provider, 500)
	assertCounts([3]int{5, 2, 7})
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_loss_event`); err != nil {
		t.Fatal(err)
	}
	retried := decode(assertRequest("POST", "/wagering/transactions", rollbackBody, "rollback-key", provider, 200))
	if retried.IdempotentReplay || retried.Balance.Amount != "20.00" {
		t.Fatal("Retry após rollback não processou LOSS")
	}
	assertCounts([3]int{6, 2, 8})
	loaded := assertRequest("GET", "/wallets/"+w.ID, "", "", internal, 200)
	if err := json.Unmarshal(loaded, &w); err != nil || w.Balance.Amount != "20.00" || w.Version != 2 {
		t.Fatal("LOSS alterou a versão após BET")
	}
}
