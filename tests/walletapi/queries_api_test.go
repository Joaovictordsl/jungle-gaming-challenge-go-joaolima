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

func TestQueriesLedgerAndReconciliationWithRealKeycloakAndPostgres(t *testing.T) {
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
	created := assertRequest("POST", "/wallets", `{"playerId":"read-player","initialBalance":{"amount":"100.00","currency":"BRL"}}`, "", internal, 201)
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
	path := "/wagering/transactions"
	bet := decode(assertRequest("POST", path, body("BET", "bet-1", "25.00", ""), "bet-key", provider, 200))
	win := decode(assertRequest("POST", path, body("WIN", "win-1", "10.00", "bet-1"), "win-key", provider, 200))
	assertRequest("POST", path, body("LOSS", "loss-1", "0.00", ""), "loss-key", provider, 200)
	assertRequest("POST", path, body("REFUND", "refund-1", "25.00", "bet-1"), "refund-key", provider, 200)
	assertRequest("POST", path, body("ROLLBACK", "rollback-1", "10.00", "win-1"), "rollback-key", provider, 200)
	rejected := decode(assertRequest("POST", path, body("BET", "rejected", "1000.00", ""), "rejected-key", provider, 422))
	byID := path + "/" + bet.TransactionID
	assertRequest("GET", byID, "", "", "", 401)
	assertRequest("GET", byID, "", "", "invalid", 401)
	transaction := assertRequest("GET", byID, "", "", provider, 200)
	if !strings.Contains(string(transaction), `"balance":{"amount":"75.00","currency":"BRL"}`) || strings.Contains(string(transaction), "idempotencyKey") || strings.Contains(string(transaction), "payloadHash") {
		t.Fatal("Consulta alterou resultado original ou expôs metadados privados")
	}
	byExternal := assertRequest("GET", "/providers/provider-a/wagering/transactions/bet-1", "", "", provider, 200)
	if string(transaction) != string(byExternal) {
		t.Fatal("Consultas por IDs divergiram")
	}
	assertRequest("GET", "/providers/provider-b/wagering/transactions/bet-1", "", "", provider, 403)
	assertRequest("GET", path+"/opening:"+w.ID, "", "", provider, 404)
	assertRequest("GET", path+"/opening:"+w.ID, "", "", internal, 200)
	assertRequest("GET", path+"/absent", "", "", internal, 404)
	rejection := assertRequest("GET", path+"/"+rejected.TransactionID, "", "", provider, 200)
	if !strings.Contains(string(rejection), `"failureCode":"INSUFFICIENT_FUNDS"`) {
		t.Fatal("Consulta omitiu rejeição")
	}
	readWin := assertRequest("GET", path+"/"+win.TransactionID, "", "", internal, 200)
	if !strings.Contains(string(readWin), `"referenceExternalTransactionId":"bet-1"`) || !strings.Contains(string(readWin), `"referenceTransactionId":"`+bet.TransactionID+`"`) {
		t.Fatal("Consulta omitiu referência resolvida")
	}
	type entry struct {
		ID, Kind, Direction string
		WalletVersion       int64
	}
	type page struct {
		WalletID        string
		SnapshotVersion int64
		Entries         []entry
		NextCursor      string
	}
	decodePage := func(data []byte) page {
		t.Helper()
		var p page
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ledgerPath := "/wallets/" + w.ID + "/ledger"
	assertRequest("GET", ledgerPath, "", "", provider, 403)
	assertRequest("POST", "/wallets/"+w.ID+"/reconciliation", "", "", provider, 403)
	assertRequest("GET", "/metrics", "", "", provider, 403)
	for _, params := range []string{"?limit=0", "?limit=101", "?limit=1&limit=2", "?cursor=invalid", "?cursor=", "?extra=1"} {
		assertRequest("GET", ledgerPath+params, "", "", internal, 400)
	}
	p := decodePage(assertRequest("GET", ledgerPath+"?limit=2", "", "", internal, 200))
	if len(p.Entries) != 2 || p.Entries[0].Kind != "OPENING" || p.SnapshotVersion != 5 || p.NextCursor == "" {
		t.Fatal("Extrato inicial incorreto")
	}
	firstCursor := p.NextCursor
	entries := append([]entry{}, p.Entries...)
	assertRequest("POST", path, body("WIN", "new-win", "1.00", ""), "new-win-key", provider, 200)
	for p.NextCursor != "" {
		p = decodePage(assertRequest("GET", ledgerPath+"?limit=2&cursor="+url.QueryEscape(p.NextCursor), "", "", internal, 200))
		if p.SnapshotVersion != 5 {
			t.Fatal("Snapshot mudou entre páginas")
		}
		entries = append(entries, p.Entries...)
	}
	if len(entries) != 5 {
		t.Fatal("Extrato duplicou ou incluiu movimentação posterior")
	}
	for i, e := range entries {
		if e.WalletVersion != int64(i+1) {
			t.Fatal("Ordem do extrato instável")
		}
	}
	full := decodePage(assertRequest("GET", ledgerPath, "", "", internal, 200))
	if len(full.Entries) != 6 || full.NextCursor != "" {
		t.Fatal("Nova leitura não incluiu crédito")
	}
	zeroWallet := assertRequest("POST", "/wallets", `{"playerId":"zero-read-player","initialBalance":{"amount":"0.00","currency":"BRL"}}`, "", internal, 201)
	var zero apiWallet
	if err := json.Unmarshal(zeroWallet, &zero); err != nil {
		t.Fatal(err)
	}
	assertRequest("GET", "/wallets/"+zero.ID+"/ledger?cursor="+url.QueryEscape(firstCursor), "", "", internal, 400)
	empty := decodePage(assertRequest("GET", "/wallets/"+zero.ID+"/ledger", "", "", internal, 200))
	if empty.Entries == nil || len(empty.Entries) != 0 || empty.NextCursor != "" {
		t.Fatal("Extrato vazio inválido")
	}
	assertRequest("GET", "/wallets/absent/ledger", "", "", internal, 404)
	assertRequest("POST", "/wallets/absent/reconciliation", "", "", internal, 404)
	assertRequest("POST", "/wallets/"+w.ID+"/reconciliation", `{}`, "", internal, 400)
	pending := decode(assertRequest("POST", path, body("WIN", "pending-win", "1.00", "future-bet"), "pending-key", provider, 202))
	readPending := assertRequest("GET", path+"/"+pending.TransactionID, "", "", provider, 200)
	if !strings.Contains(string(readPending), `"status":"PENDING_REFERENCE"`) {
		t.Fatal("Consulta não expôs pendência")
	}
	assertRequest("POST", path, body("BET", "future-bet", "1.00", ""), "future-key", provider, 200)
	deadline := time.Now().Add(10 * time.Second)
	for {
		data := assertRequest("GET", path+"/"+pending.TransactionID, "", "", provider, 200)
		if strings.Contains(string(data), `"status":"PROCESSED"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Consulta não acompanhou retomada")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	countRecords := func() [3]int {
		t.Helper()
		var counts [3]int
		if err := pool.QueryRow(ctx, `SELECT(SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM outbox_events)`).Scan(&counts[0], &counts[1], &counts[2]); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	before := countRecords()
	audit := assertRequest("POST", "/wallets/"+w.ID+"/reconciliation", "", "", internal, 200)
	if !strings.Contains(string(audit), `"consistent":true`) || !strings.Contains(string(audit), `"calculatedBalance":{"amount":"101.00","currency":"BRL"}`) || !strings.Contains(string(audit), `"checkedEntries":8`) {
		t.Fatalf("Reconciliação incorreta: %s", audit)
	}
	// Simula corrupção somente no schema descartável do teste; a API não a repara.
	injection, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer injection.Rollback(ctx)
	if _, err := injection.Exec(ctx, `ALTER TABLE wallets DISABLE TRIGGER wallet_movement`); err != nil {
		t.Fatal(err)
	}
	if _, err := injection.Exec(ctx, `UPDATE wallets SET balance_cents=balance_cents+1 WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := injection.Exec(ctx, `ALTER TABLE wallets ENABLE TRIGGER wallet_movement`); err != nil {
		t.Fatal(err)
	}
	if err := injection.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	divergent := assertRequest("POST", "/wallets/"+w.ID+"/reconciliation", "", "", internal, 200)
	if !strings.Contains(string(divergent), `"consistent":false`) || !strings.Contains(string(divergent), `"difference":{"amount":"0.01","currency":"BRL"}`) {
		t.Fatalf("Divergência não detectada: %s", divergent)
	}
	metrics := assertRequest("GET", "/metrics", "", "", internal, 200)
	if !strings.Contains(string(metrics), "jungle_reconciliation_checks_total 2\n") || !strings.Contains(string(metrics), "jungle_reconciliation_divergences_total 1\n") {
		t.Fatal("Métricas não refletem reconciliações")
	}
	if after := countRecords(); after != before {
		t.Fatal("Consultas criaram registros financeiros")
	}
	loaded := assertRequest("GET", "/wallets/"+w.ID, "", "", internal, 200)
	if err := json.Unmarshal(loaded, &w); err != nil || w.Balance.Amount != "101.01" || w.Version != 8 {
		t.Fatal("Reconciliação alterou saldo ou versão", err)
	}
}
