package sqs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
)

type probeInbox struct {
	calls  int
	final  bool
	err    error
	replay bool
}

func (p *probeInbox) Process(_ context.Context, _ string, _ message.Envelope, final bool, _ message.Handler) (appwager.Result, bool, error) {
	p.calls++
	p.final = final
	return appwager.Result{TransactionID: "tx", Status: "PROCESSED"}, p.replay, p.err
}

type probeBroker struct {
	deletes, releases int
	delay             int32
	deleteErr         error
	receipt           string
}

func (p *probeBroker) Receive(ctx context.Context) (*Delivery, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *probeBroker) Delete(_ context.Context, receipt string) error {
	p.deletes++
	p.receipt = receipt
	return p.deleteErr
}
func (p *probeBroker) Release(_ context.Context, receipt string, delay int32) error {
	p.releases++
	p.receipt = receipt
	p.delay = delay
	return nil
}

const testEnvelope = `{"messageId":"msg","type":"WagerTransactionRequested","occurredAt":"2026-10-19T00:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"bet","idempotencyKey":"key","walletId":"wallet","playerId":"player","roundId":"round","gameId":"game","kind":"BET","money":{"amount":"80.00","currency":"BRL"}}}`

func TestConsumerAcknowledgementPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, body, group        string
		attempt                  int
		err, deleteErr           error
		calls, deletes, releases int
		delay                    int32
		final                    bool
	}{
		{name: "commit", calls: 1, deletes: 1},
		{name: "commit followed by delete failure", calls: 1, deletes: 1, releases: 1, deleteErr: errors.New("network")},
		{name: "transient retry", calls: 1, releases: 1, attempt: 3, delay: 4, err: errors.New("database unavailable")},
		{name: "terminal audit", calls: 1, releases: 1, attempt: 5, final: true, err: &message.Failure{Code: "attempts_exhausted"}},
		{name: "conflicting message", calls: 1, releases: 1, err: message.ErrInboxConflict},
		{name: "malformed", body: `{}`, releases: 1},
		{name: "provider binding", body: strings.ReplaceAll(testEnvelope, "provider-a", "provider-b"), releases: 1},
		{name: "wallet group", group: "other-wallet", releases: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inbox := &probeInbox{err: tc.err}
			broker := &probeBroker{deleteErr: tc.deleteErr}
			c := Consumer{broker: broker, service: message.NewService(inbox, nil), cfg: config.SQS{ConsumerName: "consumer", ProviderID: "provider-a"}, metrics: metrics.NewMessaging(), logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
			body := tc.body
			if body == "" {
				body = testEnvelope
			}
			group := tc.group
			if group == "" {
				group = message.GroupID("wallet")
			}
			c.handle(context.Background(), Delivery{Body: body, Receipt: "receipt", GroupID: group, Attempt: tc.attempt})
			if inbox.calls != tc.calls || inbox.final != tc.final || broker.deletes != tc.deletes || broker.releases != tc.releases || broker.delay != tc.delay {
				t.Fatalf("policy: inbox=%+v broker=%+v", inbox, broker)
			}
		})
	}
}
func TestConsumerCancellationUnblocksLongPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := Consumer{broker: &probeBroker{}, cfg: config.SQS{Workers: 4}, done: make(chan struct{})}
	go c.run(ctx)
	cancel()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("pollers did not stop on cancellation")
	}
}
