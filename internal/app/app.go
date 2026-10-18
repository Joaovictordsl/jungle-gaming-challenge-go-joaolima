package app

import (
	"log/slog"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/postgres"
)

func New(options ...fx.Option) *fx.App {
	return fx.New(
		fx.Provide(config.Load, newLogger),
		fx.Provide(newWalletIDs, newWalletClock, applicationwallet.NewOpenWallet),
		postgres.Module,
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
