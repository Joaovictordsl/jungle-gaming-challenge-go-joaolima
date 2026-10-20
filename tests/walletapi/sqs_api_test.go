//go:build integration && keycloak && sqs

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
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/app"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/health"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
)

func TestSQSAndHTTPWithRealLocalStackKeycloakAndPostgres(t *testing.T) {
	ctx, database, pool := walletAPIDatabase(t)
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:4566"
	}
	// Credenciais administrativas exclusivas do simulador: filas descartáveis.
	broker := sdk.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: &http.Client{Timeout: 4 * time.Second}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String(endpoint) })
	suffix := strings.ToLower(rand.Text())
	sourceName, dlqName := "jungle-test-"+suffix+".fifo", "jungle-test-dlq-"+suffix+".fifo"
	dlq, err := broker.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: aws.String(dlqName), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		t.Fatal("LocalStack indisponível", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_, err := broker.DeleteQueue(cleanup, &sdk.DeleteQueueInput{QueueUrl: dlq.QueueUrl})
		if err != nil {
			t.Error("limpeza DLQ", err)
		}
	})
	attrs, err := broker.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: dlq.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	redrive, _ := json.Marshal(map[string]string{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": "5"})
	source, err := broker.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: aws.String(sourceName), Attributes: map[string]string{"FifoQueue": "true", "VisibilityTimeout": "30", "RedrivePolicy": string(redrive)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_, err := broker.DeleteQueue(cleanup, &sdk.DeleteQueueInput{QueueUrl: source.QueueUrl})
		if err != nil {
			t.Error("limpeza fila", err)
		}
	})
	issuer := os.Getenv("TEST_KEYCLOAK_ISSUER_URL")
	if issuer == "" {
		issuer = "http://localhost:8081/realms/jungle"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 6 * time.Second, Transport: transport}
	token := func(id, env, fallback string) string {
		t.Helper()
		secret := os.Getenv(env)
		if secret == "" {
			secret = fallback
		}
		r, err := client.PostForm(issuer+"/protocol/openid-connect/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}})
		if err != nil {
			t.Fatal("Keycloak indisponível")
		}
		defer r.Body.Close()
		var body struct {
			Token string `json:"access_token"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.StatusCode != 200 || body.Token == "" {
			t.Fatal("falha ao obter token do teste")
		}
		return body.Token
	}
	internal, provider := token("jungle-internal", "KEYCLOAK_INTERNAL_CLIENT_SECRET", "jungle_internal_local"), token("jungle-provider-a", "KEYCLOAK_PROVIDER_A_CLIENT_SECRET", "jungle_provider_a_local")
	var server *httpserver.Server
	application := app.New(fx.Replace(database, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}, config.Auth{IssuerURL: issuer, Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 3 * time.Second, ProviderClients: map[string]string{"jungle-provider-a": "provider-a"}}, config.SQS{Enabled: true, Endpoint: endpoint, Region: "us-east-1", QueueName: sourceName, DLQName: dlqName, ProviderID: "provider-a", ConsumerName: "test-consumer", AccessKeyID: "test", SecretAccessKey: "test", Workers: 4}, slog.New(slog.NewJSONHandler(io.Discard, nil))), fx.Populate(&server))
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := application.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	readiness := func(want int, wantSQS health.Status) {
		t.Helper()
		r, err := client.Get("http://" + server.Address() + "/health/ready")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var body struct {
			Status string
			Checks health.Checks
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.StatusCode != want || body.Checks.Postgres != health.OK || body.Checks.SQSConsumer != wantSQS || body.Checks.SQSPublisher != health.Disabled {
			t.Fatalf("readiness HTTP=%d checks=%+v", r.StatusCode, body.Checks)
		}
	}
	readiness(200, health.OK)

	request := func(path string, body []byte, key, bearer string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "POST", "http://"+server.Address()+path, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		if r.StatusCode != want {
			t.Fatalf("HTTP %d esperado %d: %s", r.StatusCode, want, data)
		}
		return data
	}
	var w apiWallet
	if err := json.Unmarshal(request("/wallets", []byte(`{"playerId":"mixed-player","initialBalance":{"amount":"100.00","currency":"BRL"}}`), "", internal, 201), &w); err != nil {
		t.Fatal(err)
	}
	command := func(kind, id, amount, ref string) map[string]any {
		v := map[string]any{"providerId": "provider-a", "externalTransactionId": id, "walletId": w.ID, "playerId": w.PlayerID, "roundId": "round", "gameId": "game", "kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"}}
		if ref != "" {
			v["referenceExternalTransactionId"] = ref
		}
		return v
	}
	envelope := func(id string, data map[string]any) []byte {
		copy := make(map[string]any)
		for k, v := range data {
			copy[k] = v
		}
		copy["idempotencyKey"] = data["externalTransactionId"].(string) + "-key"
		body, _ := json.Marshal(map[string]any{"messageId": id, "type": "WagerTransactionRequested", "occurredAt": "2026-10-19T00:00:00Z", "data": copy})
		return body
	}
	send := func(body []byte, group string) {
		t.Helper()
		_, err := broker.SendMessage(ctx, &sdk.SendMessageInput{QueueUrl: source.QueueUrl, MessageBody: aws.String(string(body)), MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(rand.Text())})
		if err != nil {
			t.Fatal(err)
		}
	}
	awaitInbox := func(id, status string) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			var got string
			err := pool.QueryRow(ctx, `SELECT result_status FROM inbox_messages WHERE message_id=$1`, id).Scan(&got)
			if err == nil && got == status {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatalf("inbox %s não concluiu em %s", id, status)
	}
	// HTTP primeiro, depois SQS. A duplicata tem outra entrega no broker.
	bet := command("BET", "bet", "80.00", "")
	body, _ := json.Marshal(bet)
	request("/wagering/transactions", body, "bet-key", provider, 200)
	original := envelope("msg-bet", bet)
	send(original, message.GroupID(w.ID))
	awaitInbox("msg-bet", "PROCESSED")
	send(original, message.GroupID(w.ID))
	// SQS primeiro para os cinco tipos. REFUND devolve BET; ROLLBACK desfaz WIN.
	for _, tc := range []struct{ kind, id, amount, ref string }{{"WIN", "win", "10.00", ""}, {"LOSS", "loss", "0.00", ""}, {"REFUND", "refund", "80.00", "bet"}, {"ROLLBACK", "rollback", "10.00", "win"}, {"BET", "reject", "200.00", ""}} {
		cmd := command(tc.kind, tc.id, tc.amount, tc.ref)
		id := "msg-" + tc.id
		send(envelope(id, cmd), message.GroupID(w.ID))
		status := "PROCESSED"
		want := 200
		if tc.id == "reject" {
			status = "REJECTED"
			want = 422
		}
		awaitInbox(id, status)
		body, _ := json.Marshal(cmd)
		var r struct {
			Replay bool `json:"idempotentReplay"`
		}
		if json.Unmarshal(request("/wagering/transactions", body, tc.id+"-key", provider, want), &r) != nil || !r.Replay {
			t.Fatal("HTTP reaplicou efeito da fila")
		}
	}
	// Uma referência ausente é durável antes de ACK e termina pelo worker próprio.
	pending := command("REFUND", "pending", "1.00", "future-bet")
	send(envelope("msg-pending", pending), message.GroupID(w.ID))
	awaitInbox("msg-pending", "PENDING_REFERENCE")
	future := command("BET", "future-bet", "1.00", "")
	body, _ = json.Marshal(future)
	request("/wagering/transactions", body, "future-bet-key", provider, 200)
	deadline := time.Now().Add(6 * time.Second)
	for {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE external_transaction_id='pending'`).Scan(&status); err == nil && status == "PROCESSED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("referência não retomada")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Mesmo messageId com valor alterado e corpo forjando outro provedor vão à DLQ.
	changed := envelope("msg-bet", command("BET", "bet", "79.00", ""))
	send(changed, message.GroupID(w.ID))
	forged := command("WIN", "forged", "1.00", "")
	forged["providerId"] = "provider-b"
	send(envelope("msg-forged", forged), message.GroupID(w.ID))
	send([]byte(`{"invalid":true}`), message.GroupID("invalid-wallet"))
	dlqBodies := map[string]bool{}
	deadline = time.Now().Add(8 * time.Second)
	for len(dlqBodies) < 3 && time.Now().Before(deadline) {
		out, err := broker.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: dlq.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			dlqBodies[aws.ToString(m.Body)] = true
			_, err = broker.DeleteMessage(ctx, &sdk.DeleteMessageInput{QueueUrl: dlq.QueueUrl, ReceiptHandle: m.ReceiptHandle})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(dlqBodies) != 3 || !dlqBodies[string(changed)] {
		t.Fatal("redrive não preservou as três mensagens inválidas", len(dlqBodies))
	}
	var balance, version int64
	var txs, ledger, outbox, inbox int
	err = pool.QueryRow(ctx, `SELECT balance_cents,version,(SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM outbox_events),(SELECT count(*) FROM inbox_messages) FROM wallets WHERE id=$1`, w.ID).Scan(&balance, &version, &txs, &ledger, &outbox, &inbox)
	if err != nil || balance != 10000 || version != 7 || txs != 9 || ledger != 7 || outbox != 17 || inbox != 7 {
		t.Fatalf("efeitos parciais/duplicados: saldo=%d v=%d tx=%d ledger=%d outbox=%d inbox=%d erro=%v", balance, version, txs, ledger, outbox, inbox, err)
	}
	// A fila está vazia antes de encerrar: Stop cancela os long polls ativos.
	deadline = time.Now().Add(3 * time.Second)
	for {
		a, err := broker.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: source.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		if err != nil {
			t.Fatal(err)
		}
		if a.Attributes["ApproximateNumberOfMessages"] == "0" && a.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ACK não removeu entregas confirmadas")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Degradação e recuperação do broker refletem no HTTP sem consumir mensagens.
	if _, err := broker.SetQueueAttributes(ctx, &sdk.SetQueueAttributesInput{QueueUrl: source.QueueUrl, Attributes: map[string]string{"VisibilityTimeout": "31"}}); err != nil {
		t.Fatal(err)
	}
	readiness(503, health.Unavailable)
	if _, err := broker.SetQueueAttributes(ctx, &sdk.SetQueueAttributesInput{QueueUrl: source.QueueUrl, Attributes: map[string]string{"VisibilityTimeout": "30"}}); err != nil {
		t.Fatal(err)
	}
	readiness(200, health.OK)

}
