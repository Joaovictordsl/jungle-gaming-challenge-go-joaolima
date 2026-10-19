//go:build integration && keycloak

package walletapi_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/app"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

func walletAPIDatabase(t *testing.T) (context.Context, config.Config, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("Defina TEST_DATABASE_URL para testar a API com PostgreSQL real")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL inválida")
	}
	t.Cleanup(admin.Close)
	schemaName := "wallet_api_" + strings.ToLower(rand.Text())
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL inválida")
	}
	parameters := parsed.Query()
	parameters.Set("search_path", schemaName)
	parsed.RawQuery = parameters.Encode()
	isolatedURL := parsed.String()
	pool, err := pgxpool.New(ctx, isolatedURL)
	if err != nil {
		t.Fatal("Falha ao configurar banco isolado")
	}
	t.Cleanup(pool.Close)
	for _, file := range []string{"000001_create_wallets.up.sql", "000002_wallet_opening.up.sql", "000003_bet.up.sql", "000004_win.up.sql", "000005_loss.up.sql", "000006_refund.up.sql", "000007_rollback.up.sql", "000008_ledger_queries.up.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, config.Config{DatabaseURL: isolatedURL, DatabaseConnectTimeout: 2 * time.Second, DatabaseMaxConns: 4}, pool
}

type apiWallet struct {
	ID, PlayerID string
	Version      int64
	Balance      struct{ Amount, Currency string }
}

func TestWalletAPIWithRealKeycloakAndPostgres(t *testing.T) {
	ctx, databaseConfig, pool := walletAPIDatabase(t)
	issuer := os.Getenv("TEST_KEYCLOAK_ISSUER_URL")
	if issuer == "" {
		issuer = "http://localhost:8081/realms/jungle"
	}
	secret := os.Getenv("KEYCLOAK_INTERNAL_CLIENT_SECRET")
	if secret == "" {
		secret = "jungle_internal_local"
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"jungle-internal"}, "client_secret": {secret}}
	tokenResponse, err := client.PostForm(issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatal("Keycloak inacessível; inicie o serviço antes dos testes")
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	decodeErr := json.NewDecoder(tokenResponse.Body).Decode(&token)
	tokenResponse.Body.Close()
	if tokenResponse.StatusCode != 200 || decodeErr != nil || token.AccessToken == "" {
		t.Fatal("Não foi possível obter token do serviço interno")
	}
	var server *httpserver.Server
	application := app.New(fx.Replace(databaseConfig,
		config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second},
		config.Auth{IssuerURL: issuer, Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 3 * time.Second},
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
	baseURL := "http://" + server.Address()
	request := func(method, path, body, bearer string, want int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("Chamada à API falhou: %v", err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s retornou %d, esperava %d; erro %v", method, path, response.StatusCode, want, err)
		}
		return data, response.Header
	}
	counts := func(walletID string, want [4]int) {
		t.Helper()
		var got [4]int
		err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM wallets WHERE id=$1),
			(SELECT count(*) FROM wager_transactions WHERE wallet_id=$1),
			(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1),
			(SELECT count(*) FROM outbox_events WHERE aggregate_id=$1)`, walletID).Scan(&got[0], &got[1], &got[2], &got[3])
		if err != nil || got != want {
			t.Fatalf("Estado financeiro incorreto: %v, esperado %v, erro %v", got, want, err)
		}
	}
	body := `{"playerId":"player-api","initialBalance":{"amount":"000100.05","currency":"BRL"}}`
	request(http.MethodPost, "/wallets", body, "", 401)
	request(http.MethodPost, "/wallets", body, "invalid-token", 401)
	var total int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM wallets").Scan(&total); err != nil || total != 0 {
		t.Fatal("Requisições não autenticadas não podem criar carteiras")
	}
	data, headers := request(http.MethodPost, "/wallets", body, token.AccessToken, 201)
	var created apiWallet
	if json.Unmarshal(data, &created) != nil || created.ID == "" || created.PlayerID != "player-api" || created.Balance.Amount != "100.05" || created.Balance.Currency != "BRL" || created.Version != 1 {
		t.Fatal("Resposta da abertura não preservou o estado financeiro")
	}
	if headers.Get("Location") != "/wallets/"+created.ID {
		t.Fatal("Location deve apontar para a nova carteira")
	}
	counts(created.ID, [4]int{1, 1, 1, 2})
	loaded, _ := request(http.MethodGet, headers.Get("Location"), "", token.AccessToken, 200)
	if string(loaded) != string(data) {
		t.Fatal("Consulta deve devolver o mesmo estado confirmado pela abertura")
	}
	request(http.MethodGet, headers.Get("Location"), "", "", 401)
	request(http.MethodGet, "/wallets/missing-wallet", "", token.AccessToken, 404)
	request(http.MethodPost, "/wallets", body, token.AccessToken, 409)
	counts(created.ID, [4]int{1, 1, 1, 2})
	for _, tc := range []struct {
		body, amount string
		records      [4]int
	}{
		{`{"playerId":"player-api","initialBalance":{"amount":"0.00","currency":"USD"}}`, "0.00", [4]int{1, 0, 0, 0}},
		{`{"playerId":"max-player","initialBalance":{"amount":"92233720368547758.07","currency":"USD"}}`, "92233720368547758.07", [4]int{1, 1, 1, 2}},
	} {
		data, _ := request(http.MethodPost, "/wallets", tc.body, token.AccessToken, 201)
		var result apiWallet
		if json.Unmarshal(data, &result) != nil || result.Balance.Amount != tc.amount {
			t.Fatal("Abertura alterou a precisão do valor")
		}
		counts(result.ID, tc.records)
	}
	request(http.MethodPost, "/wallets", `{"playerId":"invalid-player","initialBalance":{"amount":1.25,"currency":"BRL"}}`, token.AccessToken, 400)

	// Falha real no segundo evento: o HTTP não pode reportar 201 nem deixar
	// carteira, OPENING, ledger ou o primeiro evento parcialmente confirmados.
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_api_outbox() RETURNS TRIGGER LANGUAGE plpgsql AS $$
		BEGIN IF NEW.event_type = 'WalletBalanceChanged' AND NEW.payload->'data'->'money'->>'amount' = '99.99' THEN
		RAISE EXCEPTION 'injected outbox failure' USING ERRCODE = '23514'; END IF; RETURN NEW; END; $$;
		CREATE TRIGGER fail_api_outbox BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_api_outbox()`)
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/wallets", `{"playerId":"rollback-player","initialBalance":{"amount":"99.99","currency":"BRL"}}`, token.AccessToken, 500)
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM wallets WHERE player_id IN ('rollback-player','invalid-player')").Scan(&total); err != nil || total != 0 {
		t.Fatal("Falha ou entrada inválida deixou dados financeiros persistidos")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM wager_transactions").Scan(&total); err != nil || total != 2 {
		t.Fatal("Falha no HTTP deixou transações parciais")
	}
}
