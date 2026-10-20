package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type stateError string

func (e stateError) Error() string    { return "private SQL detail" }
func (e stateError) SQLState() string { return string(e) }

func TestFinancialConcurrentCountersHistogramAndBoundedLabels(t *testing.T) {
	var m Financial
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { m.Observe("http", "BET", wager.Processed, true, nil, 20*time.Millisecond) })
	}
	wg.Wait()
	m.Observe("http", "BET", "", false, fmt.Errorf("wrapped: %w", stateError("40001")), time.Second)
	m.Observe("sqs", "WIN", wager.Failed, true, errors.New("private infrastructure detail"), time.Second)
	m.Observe("http", "BET", "", false, wager.ErrIdempotencyConflict, time.Second)
	m.Observe("http", "BET", "", false, wager.ErrExternalConflict, time.Second)
	m.Observe("sqs", "WIN", "", false, stateError("40P01"), time.Second)
	m.Observe("private-transport", "private-wallet", "private-status", true, nil, time.Second)
	text := m.Prometheus()
	for _, want := range []string{
		`jungle_wager_responses_total{transport="http",kind="BET",status="PROCESSED"} 100`,
		`jungle_wager_responses_total{transport="sqs",kind="WIN",status="FAILED"} 1`,
		`jungle_wager_duplicates_total{transport="http"} 100`,
		`jungle_wager_conflicts_total{transport="http",reason="serialization"} 1`,
		`jungle_wager_conflicts_total{transport="sqs",reason="deadlock"} 1`,
		`jungle_wager_conflicts_total{transport="http",reason="idempotency"} 1`,
		`jungle_wager_conflicts_total{transport="http",reason="external_transaction"} 1`,
		`jungle_wager_processing_seconds_bucket{transport="http",kind="BET",le="0.01"} 0`,
		`jungle_wager_processing_seconds_bucket{transport="http",kind="BET",le="0.025"} 100`,
		`jungle_wager_processing_seconds_bucket{transport="http",kind="BET",le="+Inf"} 103`,
		`jungle_wager_processing_seconds_count{transport="http",kind="BET"} 103`,
	} {
		if !strings.Contains(text, want+"\n") {
			t.Fatalf("métrica ausente: %s", want)
		}
	}
	if strings.Contains(text, "private") {
		t.Fatal("métrica expôs dados ou labels livres")
	}
}

func TestCollectorParallelFailurePrivacyAndCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	probe := func(ctx context.Context) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return "sample 1\n", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	collector := NewCollector(func(context.Context) (string, error) {
		return "secret_partial_sample 1\n", errors.New("private password")
	}, probe, probe)
	result := make(chan string, 1)
	go func() { result <- collector.Prometheus(context.Background()) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probes não iniciaram em paralelo")
		}
	}
	close(release)
	text := <-result
	if !strings.Contains(text, `jungle_metrics_source_up{source="postgres"} 0`) || strings.Contains(text, "secret") || strings.Contains(text, "password") || !strings.Contains(text, `jungle_metrics_source_up{source="sqs_consumer"} 1`) {
		t.Fatal("scrape falhou ou expôs erro")
	}
	hanging := NewCollector(func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	at := time.Now()
	if text := hanging.Prometheus(ctx); !strings.Contains(text, `jungle_metrics_source_up{source="postgres"} 0`) || time.Since(at) > time.Second {
		t.Fatal("scrape ignorou cancelamento")
	}
}
