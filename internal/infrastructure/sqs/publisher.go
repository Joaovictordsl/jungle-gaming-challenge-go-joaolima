package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	"jungle-gaming-challenge-go-joaolima/internal/application/outbox"
	"jungle-gaming-challenge-go-joaolima/internal/config"
)

var PublisherModule = fx.Module("outbox-publisher", fx.Provide(config.LoadOutbox, NewPublisherClient, func(c *PublisherClient) outbox.Sender { return c }, NewPublisherWorker), fx.Invoke(func(*PublisherWorker) {}))

type PublisherClient struct {
	client *Client
	cfg    config.Outbox
	logger *slog.Logger
}

func NewPublisherClient(lc fx.Lifecycle, cfg config.Outbox, logger *slog.Logger) (*PublisherClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	publisher := &PublisherClient{cfg: cfg, logger: logger}
	if !cfg.Enabled {
		return publisher, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = cfg.Workers + 2
	api := sdk.NewFromConfig(aws.Config{Region: cfg.Region, Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""), HTTPClient: &http.Client{Transport: transport, Timeout: 6 * time.Second}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint) })
	publisher.client = &Client{API: api, transport: transport}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		err := publisher.client.Check(ctx, config.SQS{QueueName: cfg.QueueName, DLQName: cfg.DLQName})
		if err != nil {
			transport.CloseIdleConnections()
		}
		return err
	}, OnStop: func(context.Context) error { transport.CloseIdleConnections(); return nil }})
	return publisher, nil
}
func (c *PublisherClient) Ready(ctx context.Context) error {
	if c == nil {
		return errors.New("publisher SQS não iniciado")
	}
	return c.client.Ready(ctx)
}
func EventDeduplicationID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
func (c *PublisherClient) Send(ctx context.Context, d outbox.Delivery) error {
	if c.client == nil || c.client.QueueURL == "" {
		return errors.New("publisher SQS desabilitado ou não iniciado")
	}
	_, err := c.client.API.SendMessage(ctx, &sdk.SendMessageInput{QueueUrl: aws.String(c.client.QueueURL), MessageBody: aws.String(string(d.Payload)), MessageGroupId: aws.String(message.GroupID(d.AggregateID)), MessageDeduplicationId: aws.String(EventDeduplicationID(d.ID)), MessageAttributes: map[string]types.MessageAttributeValue{
		"eventType": {DataType: aws.String("String"), StringValue: aws.String(d.EventType)}, "version": {DataType: aws.String("Number"), StringValue: aws.String("1")}, "publicationSequence": {DataType: aws.String("Number"), StringValue: aws.String(strconv.FormatInt(d.Sequence, 10))},
	}})
	if c.logger != nil {
		if err == nil {
			c.logger.Info("Evento aceito pelo SQS", "eventId", d.ID, "correlationId", d.TransactionID, "transactionId", d.TransactionID, "walletId", d.AggregateID, "providerId", d.ProviderID, "eventType", d.EventType)
		} else {
			c.logger.Warn("Envio de evento será repetido", "eventId", d.ID, "transactionId", d.TransactionID, "walletId", d.AggregateID, "error_code", "outbox_send_unavailable")
		}
	}
	return err
}

type PublisherWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewPublisherWorker(lc fx.Lifecycle, cfg config.Outbox, service *outbox.Service, logger *slog.Logger) *PublisherWorker {
	w := &PublisherWorker{done: make(chan struct{})}
	if !cfg.Enabled {
		return w
	}
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		go func() {
			defer close(w.done)
			var workers sync.WaitGroup
			for range cfg.Workers {
				workers.Go(func() { runPublisher(ctx, service, logger) })
			}
			workers.Go(func() {
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					_ = service.RefreshBacklog(ctx)
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			})
			workers.Wait()
		}()
		logger.Info("Publisher da outbox iniciado", "workers", cfg.Workers)
		return nil
	}, OnStop: func(ctx context.Context) error {
		w.cancel()
		select {
		case <-w.done:
			logger.Info("Publisher da outbox encerrado")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return w
}
func runPublisher(ctx context.Context, service *outbox.Service, logger *slog.Logger) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		worked, err := service.RunOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := 250 * time.Millisecond
		if err != nil {
			logger.Warn("Publicação da outbox será retomada", "error_code", "outbox_publish_unavailable")
			delay = time.Second
		} else if worked {
			delay = 0
		}
		timer.Reset(delay)
	}
}
