package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.uber.org/fx"

	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
)

var Module = fx.Module("reference-worker", fx.Provide(NewReferenceWorker), fx.Invoke(func(*ReferenceWorker) {}))

type ReferenceWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewReferenceWorker(lifecycle fx.Lifecycle, processor applicationwager.ReferenceProcessor, logger *slog.Logger) *ReferenceWorker {
	worker := &ReferenceWorker{done: make(chan struct{})}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			worker.cancel = cancel
			go func() { defer close(worker.done); runReferences(ctx, processor, logger) }()
			logger.Info("Worker de referências iniciado")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			worker.cancel()
			select {
			case <-worker.done:
				logger.Info("Worker de referências encerrado")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	return worker
}

func runReferences(ctx context.Context, processor applicationwager.ReferenceProcessor, logger *slog.Logger) {
	delay := time.Duration(0)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		worked, err := processor.RetryDueReference(ctx)
		delay = time.Second
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			// Erros do driver podem incluir SQL ou credenciais. Não registre a causa bruta.
			logger.Error("Falha ao retomar referência", "error_code", "reference_retry_unavailable")
		} else if worked {
			delay = 0
		}
		timer.Reset(delay)
	}
}
