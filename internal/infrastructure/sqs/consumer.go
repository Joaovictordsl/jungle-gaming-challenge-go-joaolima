package sqs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
)

var Module = fx.Module("sqs", fx.Provide(config.LoadSQS, NewClient, NewConsumer), fx.Invoke(func(*Consumer) {}))

type Consumer struct {
	broker  Broker
	service *message.Service
	cfg     config.SQS
	logger  *slog.Logger
	metrics *metrics.Messaging
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewConsumer(lc fx.Lifecycle, c *Client, service *message.Service, cfg config.SQS, logger *slog.Logger, m *metrics.Messaging) *Consumer {
	worker := &Consumer{broker: c, service: service, cfg: cfg, logger: logger, metrics: m, done: make(chan struct{})}
	if cfg.Enabled {
		lc.Append(fx.Hook{OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			worker.cancel = cancel
			go worker.run(ctx)
			logger.Info("Consumidor SQS iniciado", "workers", cfg.Workers, "providerId", cfg.ProviderID)
			return nil
		}, OnStop: func(ctx context.Context) error {
			worker.cancel()
			select {
			case <-worker.done:
				logger.Info("Consumidor SQS encerrado")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	}
	return worker
}
func (c *Consumer) run(ctx context.Context) {
	defer close(c.done)
	var workers sync.WaitGroup
	for range c.cfg.Workers {
		workers.Go(func() { c.poll(ctx) })
	}
	workers.Wait()
}
func (c *Consumer) poll(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		delivery, err := c.broker.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			delay := time.Second * time.Duration(1<<min(failures-1, 5))
			c.logger.Warn("Falha ao receber SQS", "error_code", "sqs_receive_unavailable")
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		failures = 0
		if delivery == nil {
			continue
		}
		if ctx.Err() != nil {
			c.release(delivery.Receipt, 0)
			return
		}
		c.handle(ctx, *delivery)
	}
}
func (c *Consumer) handle(ctx context.Context, d Delivery) {
	c.metrics.Received()
	e, err := message.Parse([]byte(d.Body))
	code := ""
	if err != nil {
		code = "invalid_envelope"
	} else if e.Command.ProviderID != c.cfg.ProviderID {
		code = "provider_mismatch"
	} else if d.GroupID != message.GroupID(e.Command.WalletID) {
		code = "message_group_mismatch"
	}
	if code != "" {
		c.metrics.Failed()
		c.logger.Warn("Mensagem SQS inválida", "messageId", d.BrokerID, "error_code", code, "attempt", d.Attempt)
		c.release(d.Receipt, 0)
		return
	}
	started := time.Now()
	result, replay, err := c.service.Execute(ctx, c.cfg.ConsumerName, e, d.Attempt >= MaxReceives)
	c.metrics.Financial.Observe("sqs", e.Command.Kind, result.Status, replay || result.FinancialReplay, err, time.Since(started))
	if err != nil {
		if ctx.Err() != nil {
			c.release(d.Receipt, 0)
			return
		}
		code = message.PermanentCode(err)
		delay := int32(1 << min(max(d.Attempt-1, 0), 4))
		if code != "" {
			delay = 0
			c.metrics.Failed()
		} else {
			code = "processing_unavailable"
			c.metrics.Retry()
		}
		c.logger.Warn("Tratamento SQS não confirmado", "messageId", e.MessageID, "correlationId", result.TransactionID, "transactionId", result.TransactionID, "walletId", e.Command.WalletID, "providerId", c.cfg.ProviderID, "error_code", code, "attempt", d.Attempt)
		c.release(d.Receipt, delay)
		return
	}
	c.metrics.Completed(replay)
	ack, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := c.broker.Delete(ack, d.Receipt); err != nil {
		c.metrics.DeleteFailed()
		c.logger.Warn("Commit SQS confirmado; remoção será repetida", "messageId", e.MessageID, "transactionId", result.TransactionID, "error_code", "sqs_delete_unavailable")
		c.release(d.Receipt, 0)
		return
	}
	c.logger.Info("Mensagem SQS concluída", "messageId", e.MessageID, "correlationId", result.TransactionID, "transactionId", result.TransactionID, "walletId", e.Command.WalletID, "providerId", c.cfg.ProviderID, "status", result.Status, "inboxReplay", replay, "idempotentReplay", result.FinancialReplay)
}
func (c *Consumer) release(receipt string, delay int32) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.broker.Release(ctx, receipt, delay); err != nil {
		c.logger.Warn("Visibilidade SQS expira no broker", "error_code", "sqs_release_unavailable")
	}
}
