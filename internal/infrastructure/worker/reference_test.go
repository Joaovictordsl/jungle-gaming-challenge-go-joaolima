package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.uber.org/fx"

	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
)

type referenceProcessorFunc func(context.Context) (bool, error)

func (f referenceProcessorFunc) RetryDueReference(ctx context.Context) (bool, error) { return f(ctx) }

func TestReferenceWorkerCancelsInFlightWorkBeforeStopping(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	processor := referenceProcessorFunc(func(ctx context.Context) (bool, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return false, ctx.Err()
	})
	var worker *ReferenceWorker
	application := fx.New(Module, fx.Supply(fx.Annotate(processor, fx.As(new(applicationwager.ReferenceProcessor)))),
		fx.Supply(slog.New(slog.NewJSONHandler(io.Discard, nil))), fx.Populate(&worker), fx.NopLogger)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("Worker não iniciou")
	}
	if err := application.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("Shutdown não cancelou a operação em andamento")
	}
	select {
	case <-worker.done:
	default:
		t.Fatal("Shutdown retornou com worker ainda executando")
	}
}
