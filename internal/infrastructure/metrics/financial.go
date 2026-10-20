package metrics

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

var kinds = [...]string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"}
var transports = [...]string{"http", "sqs"}
var responseStatuses = [...]string{"PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED", "ERROR"}
var latencyBounds = [...]time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}

type histogram struct {
	buckets [len(latencyBounds)]uint64
	count   uint64
	sum     time.Duration
}

// Labels são enums fixos; nenhum ID, payload ou mensagem de erro entra nas séries.
type Financial struct {
	mu         sync.Mutex
	responses  [len(transports)][len(kinds)][len(responseStatuses)]uint64
	latency    [len(transports)][len(kinds)]histogram
	duplicates [len(transports)]uint64
	conflicts  [len(transports)][4]uint64
}

func index(values []string, value string) int {
	for i, candidate := range values {
		if value == candidate {
			return i
		}
	}
	return -1
}

func (m *Financial) Observe(transport, kind, status string, replay bool, err error, elapsed time.Duration) {
	ti, ki := index(transports[:], transport), index(kinds[:], kind)
	if ti < 0 || ki < 0 {
		return
	}
	si := index(responseStatuses[:], status)
	if si < 0 || (err != nil && status != wager.Failed) {
		si = len(responseStatuses) - 1
	}
	elapsed = max(0, elapsed)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses[ti][ki][si]++
	if replay {
		m.duplicates[ti]++
	}
	ci := -1
	switch {
	case errors.Is(err, wager.ErrIdempotencyConflict):
		ci = 0
	case errors.Is(err, wager.ErrExternalConflict):
		ci = 1
	default:
		var sqlState interface{ SQLState() string }
		if errors.As(err, &sqlState) {
			switch sqlState.SQLState() {
			case "40001":
				ci = 2
			case "40P01":
				ci = 3
			}
		}
	}
	if ci >= 0 {
		m.conflicts[ti][ci]++
	}
	h := &m.latency[ti][ki]
	h.count++
	h.sum += elapsed
	for i, bound := range latencyBounds {
		if elapsed <= bound {
			h.buckets[i]++
		}
	}
}

func (m *Financial) Prometheus() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out strings.Builder
	out.WriteString("# TYPE jungle_wager_responses_total counter\n# TYPE jungle_wager_duplicates_total counter\n# TYPE jungle_wager_conflicts_total counter\n# TYPE jungle_wager_processing_seconds histogram\n")
	for ti, transport := range transports {
		fmt.Fprintf(&out, "jungle_wager_duplicates_total{transport=%q} %d\n", transport, m.duplicates[ti])
		for ci, reason := range []string{"idempotency", "external_transaction", "serialization", "deadlock"} {
			fmt.Fprintf(&out, "jungle_wager_conflicts_total{transport=%q,reason=%q} %d\n", transport, reason, m.conflicts[ti][ci])
		}
		for ki, kind := range kinds {
			for si, status := range responseStatuses {
				fmt.Fprintf(&out, "jungle_wager_responses_total{transport=%q,kind=%q,status=%q} %d\n", transport, kind, status, m.responses[ti][ki][si])
			}
			h := m.latency[ti][ki]
			for bi, bound := range latencyBounds {
				fmt.Fprintf(&out, "jungle_wager_processing_seconds_bucket{transport=%q,kind=%q,le=%q} %d\n", transport, kind, fmt.Sprintf("%g", bound.Seconds()), h.buckets[bi])
			}
			fmt.Fprintf(&out, "jungle_wager_processing_seconds_bucket{transport=%q,kind=%q,le=\"+Inf\"} %d\njungle_wager_processing_seconds_count{transport=%q,kind=%q} %d\njungle_wager_processing_seconds_sum{transport=%q,kind=%q} %g\n", transport, kind, h.count, transport, kind, h.count, transport, kind, h.sum.Seconds())
		}
	}
	return out.String()
}

type TransactionCount struct {
	Kind, Status             string
	Count, ReferenceAttempts int64
}

// O snapshot SQL conta operações únicas, inclusive retomadas pelo worker. Deve
// ser agregado por max entre réplicas que leem o mesmo banco, nunca por sum.
func RenderTransactions(rows []TransactionCount) string {
	var out strings.Builder
	out.WriteString("# TYPE jungle_wager_transactions gauge\n# TYPE jungle_reference_attempts gauge\n")
	var attempts int64
	for _, kind := range append([]string{"OPENING"}, kinds[:]...) {
		for _, status := range responseStatuses[:4] {
			var count int64
			for _, row := range rows {
				if row.Kind == kind && row.Status == status {
					count += row.Count
					attempts += row.ReferenceAttempts
				}
			}
			fmt.Fprintf(&out, "jungle_wager_transactions{kind=%q,status=%q} %d\n", kind, status, count)
		}
	}
	fmt.Fprintf(&out, "jungle_reference_attempts %d\n", attempts)
	return out.String()
}
