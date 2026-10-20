//go:build integration && sqs

package sqs

import (
	"bufio"
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
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	iam "github.com/aws/aws-sdk-go-v2/service/iam"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/application/outbox"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	appwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/postgres"
)

type publisherFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	admin       *sdk.Client
	sender      *PublisherClient
	cfg         config.Outbox
	queueURL    string
	databaseURL string
}

func newPublisherFixture(t *testing.T) *publisherFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	database := os.Getenv("TEST_DATABASE_URL")
	if database == "" {
		t.Fatal("Defina TEST_DATABASE_URL")
	}
	adminDB, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal("banco inválido")
	}
	t.Cleanup(adminDB.Close)
	schemaName := "publisher_" + strings.ToLower(rand.Text())
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := adminDB.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
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
	t.Cleanup(pool.Close)
	files, _ := filepath.Glob("../../../migrations/*.up.sql")
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
	t.Cleanup(transport.CloseIdleConnections)
	awsConfig := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: &http.Client{Timeout: 6 * time.Second, Transport: transport}}
	broker := sdk.NewFromConfig(awsConfig, func(o *sdk.Options) { o.BaseEndpoint = aws.String(endpoint) })
	suffix := strings.ToLower(rand.Text())
	name, deadName := "publisher-"+suffix+".fifo", "publisher-dlq-"+suffix+".fifo"
	dead, err := broker.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: aws.String(deadName), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := broker.DeleteQueue(ctx, &sdk.DeleteQueueInput{QueueUrl: dead.QueueUrl}); err != nil {
			t.Error(err)
		}
	})
	a, err := broker.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: dead.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(map[string]string{"deadLetterTargetArn": a.Attributes["QueueArn"], "maxReceiveCount": "5"})
	queue, err := broker.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: aws.String(name), Attributes: map[string]string{"FifoQueue": "true", "VisibilityTimeout": "30", "RedrivePolicy": string(policy)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := broker.DeleteQueue(ctx, &sdk.DeleteQueueInput{QueueUrl: queue.QueueUrl}); err != nil {
			t.Error(err)
		}
	})
	source, err := broker.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: queue.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	identity := iam.NewFromConfig(awsConfig, func(o *iam.Options) { o.BaseEndpoint = aws.String(endpoint) })
	username := "publisher-test-" + suffix
	if _, err := identity.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(username)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := identity.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(username)}); err != nil {
			t.Error(err)
		}
	})
	permission, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"sqs:SendMessage"}, "Resource": source.Attributes["QueueArn"]}, map[string]any{"Effect": "Allow", "Action": []string{"sqs:GetQueueUrl", "sqs:GetQueueAttributes"}, "Resource": []string{source.Attributes["QueueArn"], a.Attributes["QueueArn"]}}}})
	if _, err := identity.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(username), PolicyName: aws.String("publisher-test"), PolicyDocument: aws.String(string(permission))}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := identity.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(username), PolicyName: aws.String("publisher-test")}); err != nil {
			t.Error(err)
		}
	})
	key, err := identity.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(username)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := identity.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(username), AccessKeyId: key.AccessKey.AccessKeyId}); err != nil {
			t.Error(err)
		}
	})
	cfg := config.Outbox{Enabled: true, Endpoint: endpoint, Region: "us-east-1", QueueName: name, DLQName: deadName, AccessKeyID: aws.ToString(key.AccessKey.AccessKeyId), SecretAccessKey: aws.ToString(key.AccessKey.SecretAccessKey), Workers: 2}
	var sender *PublisherClient
	app := fx.New(fx.Provide(NewPublisherClient), fx.Supply(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil))), fx.Populate(&sender), fx.NopLogger)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	// Esta suíte exige fiscalização IAM real; o teste não passa em modo permissivo.
	if _, err := sender.client.API.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: queue.QueueUrl, WaitTimeSeconds: 0}); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatal("publisher conseguiu consumir: IAM não está sendo fiscalizado")
	}
	return &publisherFixture{ctx: ctx, pool: pool, admin: broker, sender: sender, cfg: cfg, queueURL: aws.ToString(queue.QueueUrl), databaseURL: parsed.String()}
}
func (f *publisherFixture) open(t *testing.T, id string) {
	t.Helper()
	amount, _ := money.New("100.00", "BRL")
	w, err := wallet.NewWallet(id, "player-"+id, amount, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewOpeningStore(f.pool).SaveOpening(f.ctx, w); err != nil {
		t.Fatal(err)
	}
}
func (f *publisherFixture) receive(t *testing.T, want int) map[string]string {
	t.Helper()
	received := map[string]string{}
	for len(received) < want {
		out, err := f.admin.ReceiveMessage(f.ctx, &sdk.ReceiveMessageInput{QueueUrl: aws.String(f.queueURL), WaitTimeSeconds: 1, MaxNumberOfMessages: 10, MessageAttributeNames: []string{"All"}, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Messages) == 0 {
			t.Fatalf("eventos ausentes: %d/%d", len(received), want)
		}
		for _, m := range out.Messages {
			var e struct {
				EventID, EventType, AggregateID, CorrelationID string
				Version                                        int
			}
			if json.Unmarshal([]byte(aws.ToString(m.Body)), &e) != nil || e.EventID == "" || e.Version != 1 {
				t.Fatal("envelope inválido")
			}
			var snapshot []byte
			if err := f.pool.QueryRow(f.ctx, `SELECT payload FROM outbox_events WHERE id=$1`, e.EventID).Scan(&snapshot); err != nil || string(snapshot) != aws.ToString(m.Body) {
				t.Fatal("snapshot mudou durante envio", err)
			}
			if m.Attributes["MessageDeduplicationId"] != EventDeduplicationID(e.EventID) || m.Attributes["MessageGroupId"] != messageGroup(e.AggregateID) || aws.ToString(m.MessageAttributes["eventType"].StringValue) != e.EventType {
				t.Fatal("metadados de roteamento incorretos")
			}
			if _, ok := received[e.EventID]; ok {
				t.Fatal("entrega duplicada dentro da janela FIFO")
			}
			received[e.EventID] = aws.ToString(m.Body)
			if _, err := f.admin.DeleteMessage(f.ctx, &sdk.DeleteMessageInput{QueueUrl: aws.String(f.queueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return received
}
func messageGroup(id string) string { return "wallet:" + EventDeduplicationID(id) }

// Os processos morrem sem executar defers/hooks nos dois pontos de interrupção.
func TestOutboxPublisherProcessHelper(t *testing.T) {
	if os.Getenv("JUNGLE_OUTBOX_CHILD") != "1" {
		t.Skip("helper de processo independente")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("JUNGLE_OUTBOX_DATABASE"))
	if err != nil {
		t.Fatal("pool inválido")
	}
	defer pool.Close()
	cfg, err := config.LoadOutbox()
	if err != nil {
		t.Fatal("configuração inválida")
	}
	api := sdk.NewFromConfig(aws.Config{Region: cfg.Region, Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""), HTTPClient: &http.Client{Timeout: 5 * time.Second}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint) })
	sender := &PublisherClient{client: &Client{API: api, QueueURL: os.Getenv("JUNGLE_OUTBOX_QUEUE_URL")}}
	store := postgres.NewOutboxStore(pool)
	fmt.Println("OUTBOX_READY")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	d, found, err := store.Claim(ctx, rand.Text())
	if err != nil || !found {
		t.Fatal("claim ausente", err)
	}
	mode := os.Getenv("JUNGLE_OUTBOX_MODE")
	if mode == "before-send" {
		fmt.Println("OUTBOX_RESULT " + d.ID)
		os.Exit(0)
	}
	if err := sender.Send(ctx, d); err != nil {
		t.Fatal(err)
	}
	if mode == "after-send" {
		fmt.Println("OUTBOX_RESULT " + d.ID)
		os.Exit(0)
	}
	if err := store.Published(ctx, d); err != nil {
		t.Fatal(err)
	}
	fmt.Println("OUTBOX_RESULT " + d.ID)
}
func (f *publisherFixture) processes(t *testing.T, modes ...string) []string {
	t.Helper()
	type child struct {
		cmd     *exec.Cmd
		in      io.WriteCloser
		scanner *bufio.Scanner
	}
	children := make([]child, len(modes))
	for i, mode := range modes {
		cmd := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestOutboxPublisherProcessHelper$")
		cmd.Env = append(os.Environ(), "JUNGLE_OUTBOX_CHILD=1", "JUNGLE_OUTBOX_DATABASE="+f.databaseURL, "JUNGLE_OUTBOX_MODE="+mode, "JUNGLE_OUTBOX_QUEUE_URL="+f.queueURL, "OUTBOX_ENABLED=true", "OUTBOX_ENDPOINT="+f.cfg.Endpoint, "OUTBOX_REGION="+f.cfg.Region, "OUTBOX_QUEUE_NAME="+f.cfg.QueueName, "OUTBOX_DLQ_NAME="+f.cfg.DLQName, "OUTBOX_ACCESS_KEY_ID="+f.cfg.AccessKeyID, "OUTBOX_SECRET_ACCESS_KEY="+f.cfg.SecretAccessKey)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() || scanner.Text() != "OUTBOX_READY" {
			t.Fatal("processo não ficou pronto", scanner.Text())
		}
		children[i] = child{cmd, input, scanner}
	}
	for _, c := range children {
		_, _ = io.WriteString(c.in, "go\n")
		_ = c.in.Close()
	}
	results := make([]string, len(children))
	for i, c := range children {
		for c.scanner.Scan() {
			line := c.scanner.Text()
			if strings.HasPrefix(line, "OUTBOX_RESULT ") {
				results[i] = strings.TrimPrefix(line, "OUTBOX_RESULT ")
			} else if strings.Contains(line, "FAIL") {
				t.Error(line)
			}
		}
		if err := c.cmd.Wait(); err != nil || results[i] == "" {
			t.Fatal("processo falhou", err)
		}
	}
	return results
}
func TestPublisherRecoversBothCrashWindowsWithRealSQSIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	f.open(t, "crash-wallet")
	expire := func(id string) {
		t.Helper()
		if _, err := f.pool.Exec(f.ctx, `UPDATE outbox_events SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	before := f.processes(t, "before-send")[0]
	expire(before)
	if got := f.processes(t, "normal")[0]; got != before {
		t.Fatal("evento após commit mudou de identidade")
	}
	after := f.processes(t, "after-send")[0]
	expire(after)
	if got := f.processes(t, "normal")[0]; got != after {
		t.Fatal("republicação mudou de identidade")
	}
	got := f.receive(t, 2)
	if _, ok := got[before]; !ok {
		t.Fatal("primeiro evento não entregue")
	}
	if _, ok := got[after]; !ok {
		t.Fatal("segundo evento não entregue")
	}
	var total, attempts, published int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*),sum(attempts),count(published_at) FROM outbox_events`).Scan(&total, &attempts, &published); err != nil || total != 2 || attempts != 4 || published != 2 {
		t.Fatal("histórico de recuperação incorreto", total, attempts, published, err)
	}
}
func TestThreePublishersAndBrokerOutageWithRealSQSIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	for i := range 4 {
		f.open(t, fmt.Sprint("wallet-", i))
	}
	ids := f.processes(t, "normal", "normal", "normal")
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatal("dois processos confirmaram o mesmo evento")
	}

	// Os quatro tipos de evento usam a mesma rota de publicação, incluindo LOSS.
	amount, _ := money.New("200.00", "BRL")
	request := wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: "rejected", IdempotencyKey: "rejected-key", WalletID: "wallet-0", PlayerID: "player-wallet-0", RoundID: "round", GameID: "game", Money: amount}
	if result, _, err := appwager.NewProcessBet(postgres.NewBetStore(f.pool), liveIDs{}, liveClock{}).Execute(f.ctx, request); err != nil || result.Status() != wager.Rejected {
		t.Fatal("rejeição", err)
	}
	request.ExternalTransactionID = "pending"
	request.IdempotencyKey = "pending-key"
	request.Money, _ = money.New("1.00", "BRL")
	if result, _, err := appwager.NewProcessWin(postgres.NewWinStore(f.pool), liveIDs{}, liveClock{}).Execute(f.ctx, wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: "missing-bet"}); err != nil || result.Status() != wager.PendingReference {
		t.Fatal("pendência", err)
	}
	request.ExternalTransactionID = "loss"
	request.IdempotencyKey = "loss-key"
	request.Money, _ = money.New("0.00", "BRL")
	if _, _, err := appwager.NewProcessLoss(postgres.NewLossStore(f.pool), liveIDs{}, liveClock{}).Execute(f.ctx, wager.LossRequest{BetRequest: request}); err != nil {
		t.Fatal(err)
	}
	service := outbox.NewService(postgres.NewOutboxStore(f.pool), f.sender, liveIDs{}, &metrics.Outbox{})
	original := f.sender.client.API
	// Porta fechada: o SDK real exerce timeout/retry e erro de transporte.
	f.sender.client.API = sdk.NewFromConfig(aws.Config{Region: f.cfg.Region, Credentials: credentials.NewStaticCredentialsProvider(f.cfg.AccessKeyID, f.cfg.SecretAccessKey, ""), HTTPClient: &http.Client{Timeout: time.Second}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String("http://127.0.0.1:1"); o.RetryMaxAttempts = 1 })
	if worked, err := service.RunOnce(f.ctx); !worked || err == nil {
		t.Fatal("indisponibilidade do broker ignorada")
	}
	f.sender.client.API = original
	var pending int
	var retryAt time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*),max(next_attempt_at) FROM outbox_events WHERE published_at IS NULL`).Scan(&pending, &retryAt); err != nil || pending != 8 || retryAt.Before(time.Now()) {
		t.Fatal("falha perdeu evento ou backoff", err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE outbox_events SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	for {
		worked, err := service.RunOnce(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	received := f.receive(t, 11)
	typesSeen := map[string]bool{}
	for _, body := range received {
		var event struct{ EventType string }
		_ = json.Unmarshal([]byte(body), &event)
		typesSeen[event.EventType] = true
	}
	if len(typesSeen) != 4 {
		t.Fatal("os quatro tipos não foram publicados")
	}
	if len(received) != 11 {
		t.Fatal("eventos não recuperados após indisponibilidade")
	}
	backlog, err := postgres.NewOutboxStore(f.pool).Backlog(f.ctx)
	if err != nil || backlog.Pending != 0 {
		t.Fatal(err)
	}
	// Cancelamento antes de reservar não altera publicação.
	stopped, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := service.RunOnce(stopped); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelamento ignorado")
	}
}

func TestPublisherFxLifecycleWithRealSQSIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	f.open(t, "fx-wallet")
	var instances []*fx.App
	start := func() {
		t.Helper()
		application := fx.New(fx.Supply(f.cfg, slog.New(slog.NewJSONHandler(io.Discard, nil))), fx.Provide(NewPublisherClient, func(c *PublisherClient) outbox.Sender { return c }, func() outbox.Store { return postgres.NewOutboxStore(f.pool) }, func() appwallet.IDGenerator { return liveIDs{} }, func() outbox.Observer { return &metrics.Outbox{} }, outbox.NewService, NewPublisherWorker), fx.Invoke(func(*PublisherWorker) {}), fx.NopLogger)
		if err := application.Start(f.ctx); err != nil {
			t.Fatal(err)
		}
		instances = append(instances, application)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := application.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	await := func(want int) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			var count int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("worker não publicou os eventos")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	start()
	await(2)
	stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := instances[0].Stop(stop); err != nil {
		t.Fatal("shutdown não encerrou pollers", err)
	}
	f.open(t, "fx-restart")
	var pending int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&pending); err != nil || pending != 2 {
		t.Fatal("worker continuou após Stop", err)
	}
	start()
	await(4)
	if len(f.receive(t, 4)) != 4 {
		t.Fatal("nova instância não retomou backlog")
	}
}

func TestPublisherReadinessChecksRealQueuesWithoutChangingMessagesIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	f.open(t, "readiness-wallet")
	d, found, err := postgres.NewOutboxStore(f.pool).Claim(f.ctx, "readiness-fixture")
	if err != nil || !found {
		t.Fatal(err)
	}
	if err := f.sender.Send(f.ctx, d); err != nil {
		t.Fatal(err)
	}

	if err := f.sender.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.SetQueueAttributes(f.ctx, &sdk.SetQueueAttributesInput{QueueUrl: aws.String(f.queueURL), Attributes: map[string]string{"VisibilityTimeout": "31"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.sender.Ready(f.ctx); err == nil {
		t.Fatal("fila com configuração incorreta passou no readiness")
	}
	if _, err := f.admin.SetQueueAttributes(f.ctx, &sdk.SetQueueAttributesInput{QueueUrl: aws.String(f.queueURL), Attributes: map[string]string{"VisibilityTimeout": "30"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.sender.Ready(f.ctx); err != nil {
		t.Fatal("readiness não recuperou", err)
	}
	// Várias consultas sobre o mesmo cliente não modificam as URLs dos workers.
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { failures <- f.sender.Ready(f.ctx) })
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.sender.client.QueueURL != f.queueURL {
		t.Fatal("readiness mudou a URL do publisher")
	}
	if got := f.receive(t, 1); got[d.ID] == "" {
		t.Fatal("readiness consumiu ou removeu a mensagem de teste")
	}

}
