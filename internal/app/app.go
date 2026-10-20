package app

import (
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	"jungle-gaming-challenge-go-joaolima/internal/application/outbox"
	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/health"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/postgres"
	broker "jungle-gaming-challenge-go-joaolima/internal/infrastructure/sqs"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/worker"
)

func New(options ...fx.Option) *fx.App {
	return fx.New(
		fx.Provide(config.Load, newLogger, newReadiness, newMetricsCollector),
		fx.Provide(metrics.NewMessaging, applicationwager.NewDispatcher, message.NewService, outbox.NewService, func(m *metrics.Messaging) outbox.Observer { return &m.Outbox }),
		fx.Provide(metrics.NewReconciliation, func(m *metrics.Reconciliation) query.Observer { return m }, query.NewService),
		fx.Provide(newWalletIDs, newWalletClock, applicationwallet.NewOpenWallet, applicationwager.NewProcessBet, applicationwager.NewProcessWin, applicationwager.NewProcessLoss, applicationwager.NewProcessRefund, applicationwager.NewProcessRollback),
		postgres.Module,
		worker.Module,
		broker.Module,
		broker.PublisherModule,
		httpserver.Module,
		fx.StartTimeout(15*time.Second),
		fx.StopTimeout(10*time.Second),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			events := &fxevent.SlogLogger{Logger: logger}
			events.UseLogLevel(slog.LevelDebug)
			return events
		}),
		fx.Options(options...),
	)
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

func newReadiness(pool *pgxpool.Pool, consumer *broker.Client, publisher *broker.PublisherClient, sqsCfg config.SQS, outboxCfg config.Outbox) health.Checker {
	var input, output health.Probe
	if sqsCfg.Enabled {
		input = consumer.Ready
	}
	if outboxCfg.Enabled {
		output = publisher.Ready
	}
	return health.NewService(pool.Ping, input, output)
}

func newMetricsCollector(pool *pgxpool.Pool, consumer *broker.Client, publisher *broker.PublisherClient, sqsCfg config.SQS, outboxCfg config.Outbox) *metrics.Collector {
	var input, output metrics.Probe
	if sqsCfg.Enabled {
		input = consumer.DLQMetrics
	}
	if outboxCfg.Enabled {
		output = publisher.DLQMetrics
	}
	return metrics.NewCollector(postgres.MetricsSnapshot(pool), input, output)
}
