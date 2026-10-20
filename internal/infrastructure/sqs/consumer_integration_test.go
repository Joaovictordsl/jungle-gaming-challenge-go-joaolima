//go:build integration && sqs

package sqs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/postgres"
)

type liveIDs struct{}

func (liveIDs) NewID() (string, error) { return rand.Text(), nil }

type liveClock struct{}

func (liveClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

type injectedBroker struct {
	*Client
	failDelete bool
}

func (b *injectedBroker) Delete(ctx context.Context, receipt string) error {
	if b.failDelete {
		b.failDelete = false
		return errors.New("injected connection failure after commit")
	}
	return b.Client.Delete(ctx, receipt)
}

// Só nos testes: as esperas de backoff são verificadas nos testes unitários.
func (b *injectedBroker) Release(ctx context.Context, receipt string, _ int32) error {
	return b.Client.Release(ctx, receipt, 0)
}
func TestConsumerCommitDeleteFailureAndExhaustionWithRealBrokerIntegration(t *testing.T) {
	database := os.Getenv("TEST_DATABASE_URL")
	if database == "" {
		t.Fatal("Defina TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal("banco inválido")
	}
	defer admin.Close()
	schemaName := "sqs_" + strings.ToLower(rand.Text())
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	parsed, err := url.Parse(database)
	if err != nil {
		t.Fatal("banco inválido")
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal("pool inválido")
	}
	defer pool.Close()
	files, err := filepath.Glob("../../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:4566"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	api := sdk.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: transport}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String(endpoint) })
	suffix := strings.ToLower(rand.Text())
	dlq, err := api.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: aws.String("test-dlq-" + suffix + ".fifo"), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := api.DeleteQueue(cleanup, &sdk.DeleteQueueInput{QueueUrl: dlq.QueueUrl}); err != nil {
			t.Error(err)
		}
	}()
	attrs, err := api.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: dlq.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	redrive, _ := json.Marshal(map[string]string{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": "5"})
	source, err := api.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: aws.String("test-source-" + suffix + ".fifo"), Attributes: map[string]string{"FifoQueue": "true", "VisibilityTimeout": "30", "RedrivePolicy": string(redrive)}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := api.DeleteQueue(cleanup, &sdk.DeleteQueueInput{QueueUrl: source.QueueUrl}); err != nil {
			t.Error(err)
		}
	}()
	broker := &injectedBroker{Client: &Client{API: api, QueueURL: aws.ToString(source.QueueUrl), DLQURL: aws.ToString(dlq.QueueUrl)}, failDelete: true}
	service := message.NewService(postgres.NewInboxStore(pool), appwager.NewDispatcher(liveIDs{}, liveClock{}))
	consumer := Consumer{broker: broker, service: service, cfg: config.SQS{ConsumerName: "consumer", ProviderID: "provider-a"}, metrics: metrics.NewMessaging(), logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	balance, _ := money.New("100.00", "BRL")
	w, err := wallet.NewWallet("wallet", "player", balance, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	send := func(id string) {
		t.Helper()
		raw := strings.ReplaceAll(testEnvelope, `"msg"`, `"`+id+`"`)
		raw = strings.ReplaceAll(raw, `"bet"`, `"`+id+`"`)
		raw = strings.ReplaceAll(raw, `"key"`, `"`+id+`-key"`)
		_, err := api.SendMessage(ctx, &sdk.SendMessageInput{QueueUrl: source.QueueUrl, MessageBody: aws.String(raw), MessageGroupId: aws.String(message.GroupID("wallet")), MessageDeduplicationId: aws.String(rand.Text())})
		if err != nil {
			t.Fatal(err)
		}
	}
	receive := func(attempt int) Delivery {
		t.Helper()
		d, err := broker.Receive(ctx)
		if err != nil || d == nil {
			t.Fatal("entrega ausente", err)
		}
		if d.Attempt != attempt {
			t.Fatalf("recebimento=%d esperado=%d", d.Attempt, attempt)
		}
		return *d
	}
	state := func(wantBalance int64, wantTx, wantInbox int) {
		t.Helper()
		var balance int64
		var tx, inbox int
		err := pool.QueryRow(ctx, `SELECT balance_cents,(SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM inbox_messages) FROM wallets WHERE id='wallet'`).Scan(&balance, &tx, &inbox)
		if err != nil || balance != wantBalance || tx != wantTx || inbox != wantInbox {
			t.Fatalf("saldo=%d tx=%d inbox=%d erro=%v", balance, tx, inbox, err)
		}
	}
	send("ack-failure")
	consumer.handle(ctx, receive(1))
	state(2000, 2, 1)
	consumer.handle(ctx, receive(2))
	state(2000, 2, 1)
	if !strings.Contains(consumer.metrics.Prometheus(), "jungle_sqs_inbox_replayed_total 1\n") || !strings.Contains(consumer.metrics.Prometheus(), "jungle_sqs_delete_failed_total 1\n") {
		t.Fatal("reentrega após commit não foi observada")
	}
	// Falha em outbox deixa inbox e efeitos financeiros ausentes; retry posterior funciona.
	// WIN de 80.00 permite repetir este cenário sem insuficiência de saldo.
	sendWin := func(id string) {
		t.Helper()
		raw := strings.ReplaceAll(testEnvelope, `"msg"`, `"`+id+`"`)
		raw = strings.ReplaceAll(raw, `"bet"`, `"`+id+`"`)
		raw = strings.ReplaceAll(raw, `"key"`, `"`+id+`-key"`)
		raw = strings.ReplaceAll(raw, `"BET"`, `"WIN"`)
		_, err := api.SendMessage(ctx, &sdk.SendMessageInput{QueueUrl: source.QueueUrl, MessageBody: aws.String(raw), MessageGroupId: aws.String(message.GroupID("wallet")), MessageDeduplicationId: aws.String(rand.Text())})
		if err != nil {
			t.Fatal(err)
		}
	}
	fail := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT injected_sqs_failure CHECK(event_type<>'WalletBalanceChanged') NOT VALID`); err != nil {
			t.Fatal(err)
		}
	}
	recover := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT injected_sqs_failure`); err != nil {
			t.Fatal(err)
		}
	}
	fail()
	sendWin("retry")
	consumer.handle(ctx, receive(1))
	state(2000, 2, 1)
	recover()
	consumer.handle(ctx, receive(2))
	state(10000, 3, 2)
	fail()
	sendWin("exhausted")
	for attempt := 1; attempt <= 5; attempt++ {
		consumer.handle(ctx, receive(attempt))
		wantInbox := 2
		if attempt == 5 {
			wantInbox = 3
		}
		wantTx := 3
		if attempt == 5 {
			wantTx = 4
		}
		state(10000, wantTx, wantInbox)
	}
	recover()
	// O recebimento seguinte dispara o redrive nativo. Nenhum SendMessage manual na DLQ.
	_, err = api.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: source.QueueUrl, WaitTimeSeconds: 0, MaxNumberOfMessages: 1})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		snapshot, err := broker.Client.DLQMetrics(ctx)
		if err != nil || !strings.Contains(snapshot, `jungle_sqs_dlq_messages{queue="commands",state="visible"} 1`) {
			t.Fatal("métrica real de DLQ incorreta", err)
		}
	}
	dead, err := api.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 1, MaxNumberOfMessages: 1, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount}})
	if err != nil || len(dead.Messages) != 1 || !strings.Contains(aws.ToString(dead.Messages[0].Body), "exhausted") {
		t.Fatal("falha esgotada não chegou à DLQ", err)
	}
	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status,failure_code FROM inbox_messages WHERE message_id='exhausted'`).Scan(&status, &code); err != nil || status != "FAILED" || code != "attempts_exhausted" {
		t.Fatal("falha terminal não foi auditada", err)
	}
	var financialStatus, financialCode string
	if err := pool.QueryRow(ctx, `SELECT status,failure_code FROM wager_transactions WHERE external_transaction_id='exhausted'`).Scan(&financialStatus, &financialCode); err != nil || financialStatus != "FAILED" || financialCode != "INFRASTRUCTURE_RETRIES_EXHAUSTED" {
		t.Fatal("falha financeira terminal não auditada", err)
	}
	state(10000, 4, 3)
	// Cancelar SQL bloqueado libera a entrega sem confirmar claim ou saldo.
	send("cancelled")
	delivery := receive(1)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id='wallet' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	consumer.handle(short, delivery)
	stop()
	state(10000, 4, 3)
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	consumer.handle(ctx, receive(2))
	state(2000, 5, 4)
	// Processo independente encerra sem defers entre o commit da inbox e ACK.
	sendWin("crashed")
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsumerCrashProcessHelper$")
	child.Env = append(os.Environ(), "JUNGLE_CONSUMER_CHILD=1", "JUNGLE_CONSUMER_DATABASE="+pool.Config().ConnString(), "JUNGLE_CONSUMER_ENDPOINT="+endpoint, "JUNGLE_CONSUMER_SOURCE="+aws.ToString(source.QueueUrl), "JUNGLE_CONSUMER_DLQ="+aws.ToString(dlq.QueueUrl))
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "COMMITTED_BEFORE_DELETE") {
		t.Fatal("consumidor não atingiu o ponto de interrupção", err)
	}
	state(10000, 6, 5)
	// A API administrativa deste fixture tem timeout de 5s. Polls sem espera
	// acompanham a expiração nativa sem iniciar um long poll de 20s nesse cliente.
	var redelivery *Delivery
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		batch, err := api.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: source.QueueUrl, MaxNumberOfMessages: 1, WaitTimeSeconds: 0, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
		if err != nil {
			t.Fatal("reentrega após crash indisponível", err)
		}
		if len(batch.Messages) > 0 {
			m := batch.Messages[0]
			if m.Attributes["ApproximateReceiveCount"] != "2" {
				t.Fatal("crash não gerou segundo recebimento")
			}
			redelivery = &Delivery{BrokerID: aws.ToString(m.MessageId), Receipt: aws.ToString(m.ReceiptHandle), Body: aws.ToString(m.Body), GroupID: m.Attributes["MessageGroupId"], Attempt: 2}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if redelivery == nil {
		t.Fatal("broker não reentregou mensagem após crash")
	}
	consumer.handle(ctx, *redelivery)
	state(10000, 6, 5)
	var ledgers, events int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id=t.id),(SELECT count(*) FROM outbox_events WHERE transaction_id=t.id) FROM wager_transactions t WHERE external_transaction_id='crashed'`).Scan(&ledgers, &events); err != nil || ledgers != 1 || events != 2 {
		t.Fatal("reentrega após crash duplicou efeitos", err)
	}
	if !strings.Contains(consumer.metrics.Prometheus(), "jungle_sqs_inbox_replayed_total 2\n") {
		t.Fatal("processo novo não recuperou a inbox")
	}

}

type crashBeforeDelete struct{ *Client }

func (b *crashBeforeDelete) Delete(context.Context, string) error {
	fmt.Println("COMMITTED_BEFORE_DELETE")
	os.Exit(0)
	return nil
}

func TestConsumerCrashProcessHelper(t *testing.T) {
	if os.Getenv("JUNGLE_CONSUMER_CHILD") != "1" {
		t.Skip("helper de processo independente")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("JUNGLE_CONSUMER_DATABASE"))
	if err != nil {
		t.Fatal("pool do processo inválido")
	}
	defer pool.Close()
	api := sdk.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: &http.Client{Timeout: 5 * time.Second}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String(os.Getenv("JUNGLE_CONSUMER_ENDPOINT")) })
	client := &Client{API: api, QueueURL: os.Getenv("JUNGLE_CONSUMER_SOURCE"), DLQURL: os.Getenv("JUNGLE_CONSUMER_DLQ")}
	delivery, err := client.Receive(ctx)
	if err != nil || delivery == nil {
		t.Fatal("processo não recebeu entrega")
	}
	// Prazo curto somente para esta entrega descartável; não muda a fila nem
	// libera a mensagem após o crash. O broker expira sua visibilidade.
	if _, err := api.ChangeMessageVisibility(ctx, &sdk.ChangeMessageVisibilityInput{QueueUrl: aws.String(client.QueueURL), ReceiptHandle: aws.String(delivery.Receipt), VisibilityTimeout: 1}); err != nil {
		t.Fatal("visibilidade de teste inválida")
	}
	service := message.NewService(postgres.NewInboxStore(pool), appwager.NewDispatcher(liveIDs{}, liveClock{}))
	consumer := Consumer{broker: &crashBeforeDelete{client}, service: service, cfg: config.SQS{ConsumerName: "consumer", ProviderID: "provider-a"}, metrics: metrics.NewMessaging(), logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	consumer.handle(ctx, *delivery)
	t.Fatal("processo retornou sem interromper antes do ACK")
}
