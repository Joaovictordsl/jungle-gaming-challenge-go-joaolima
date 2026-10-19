package app

import (
	"log/slog"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/postgres"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/worker"
)

func New(options ...fx.Option) *fx.App {
	return fx.New(
		fx.Provide(config.Load, newLogger),
		fx.Provide(metrics.NewReconciliation, func(m *metrics.Reconciliation) query.Observer { return m }, query.NewService),
		fx.Provide(newWalletIDs, newWalletClock, applicationwallet.NewOpenWallet, applicationwager.NewProcessBet, applicationwager.NewProcessWin, applicationwager.NewProcessLoss, applicationwager.NewProcessRefund, applicationwager.NewProcessRollback),
		postgres.Module,
		worker.Module,
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
