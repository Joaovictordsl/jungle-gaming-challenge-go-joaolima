//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/outbox"
	"os"
	"sync"
	"testing"
	"time"
)

func outboxDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := inboxTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000010_outbox_publication.up.sql")
	return ctx, pool
}
func TestOutboxLeaseRecoveryFencingBackoffAndWalletIsolationIntegration(t *testing.T) {
	ctx, pool := outboxDatabase(t)
	store := NewOutboxStore(pool)
	a := repositoryTestWallet(t, "outbox-a", "player-a", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, a); err != nil {
		t.Fatal(err)
	}
	first, found, err := store.Claim(ctx, "first")
	if err != nil || !found || first.Attempts != 1 || first.Recovered {
		t.Fatal("claim inicial", err)
	}
	if _, found, err := store.Claim(ctx, "blocked"); err != nil || found {
		t.Fatal("o segundo evento da carteira ultrapassou a reserva")
	}
	b := repositoryTestWallet(t, "outbox-b", "player-b", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, b); err != nil {
		t.Fatal(err)
	}
	other, found, err := store.Claim(ctx, "other-wallet")
	if err != nil || !found || other.AggregateID != b.ID() {
		t.Fatal("carteira independente bloqueada", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	recovered, found, err := store.Claim(ctx, "recovered")
	if err != nil || !found || recovered.ID != first.ID || recovered.Token == first.Token || recovered.Attempts != 2 || !recovered.Recovered || string(recovered.Payload) != string(first.Payload) {
		t.Fatal("reserva abandonada não recuperou o snapshot", err)
	}
	if err := store.Published(ctx, first); !errors.Is(err, outbox.ErrLeaseLost) {
		t.Fatal("reserva antiga confirmou a publicação", err)
	}
	if err := store.Retry(ctx, first, time.Second); !errors.Is(err, outbox.ErrLeaseLost) {
		t.Fatal("reserva antiga alterou retry", err)
	}
	if err := store.Release(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(ctx, recovered, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Claim(ctx, "early"); err != nil || found {
		t.Fatal("backoff foi ignorado")
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	resumed, found, err := store.Claim(ctx, "resumed")
	if err != nil || !found || resumed.ID != first.ID || resumed.Attempts != 3 {
		t.Fatal(err)
	}
	if err := store.Published(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE outbox_events SET published_at=NULL WHERE id=$1`, `UPDATE outbox_events SET publication_sequence=publication_sequence+1000 WHERE id=$1`, `UPDATE outbox_events SET payload='{}'::jsonb WHERE id=$1`, `DELETE FROM outbox_events WHERE id=$1`} {
		if _, err := pool.Exec(ctx, sql, resumed.ID); err == nil {
			t.Fatal("histórico da publicação foi alterado", sql)
		}
	}
	next, found, err := store.Claim(ctx, "next")
	if err != nil || !found || next.AggregateID != a.ID() || next.Sequence <= first.Sequence {
		t.Fatal("ordem da carteira perdida", err)
	}
	if err := store.Published(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := store.Published(ctx, other); err != nil {
		t.Fatal(err)
	}
	backlog, err := store.Backlog(ctx)
	if err != nil || backlog.Pending != 1 || backlog.Oldest == nil {
		t.Fatal("backlog incorreto", err)
	}
	assertOpeningCounts(t, ctx, pool, a.ID(), [4]int{1, 1, 1, 2})
}
func TestOutboxConcurrentClaimsAcrossPoolsIntegration(t *testing.T) {
	ctx, pool := outboxDatabase(t)
	for i := range 12 {
		w := repositoryTestWallet(t, fmt.Sprint("wallet-", i), fmt.Sprint("player-", i), "100.00", "BRL")
		if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	// Conexões/pools distintos: a coordenação é exclusivamente no PostgreSQL.
	failures := make(chan error, 24)
	ids := make(chan string, 24)
	var wg sync.WaitGroup
	for range 4 {
		cfg := pool.Config().Copy()
		other, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(other.Close)
		wg.Go(func() {
			store := NewOutboxStore(other)
			for {
				d, found, err := store.Claim(ctx, rand.Text())
				if err != nil {
					failures <- err
					return
				}
				if !found {
					return
				}
				time.Sleep(time.Millisecond)
				if err := store.Published(ctx, d); err != nil {
					failures <- err
					return
				}
				ids <- d.ID
			}
		})
	}
	wg.Wait()
	close(failures)
	close(ids)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("reserva simultânea duplicada")
		}
		seen[id] = true
	}
	if len(seen) != 24 {
		t.Fatal("eventos não publicados", len(seen))
	}
	b, err := NewOutboxStore(pool).Backlog(ctx)
	if err != nil || b.Pending != 0 {
		t.Fatal(err)
	}
}
func TestOutboxMigrationPreservesFinancialHistoryIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "outbox-cycle", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	runOpeningMigration(t, ctx, pool, "000010_outbox_publication.up.sql")
	runOpeningMigration(t, ctx, pool, "000010_outbox_publication.down.sql")
	runOpeningMigration(t, ctx, pool, "000010_outbox_publication.up.sql")
	store := NewOutboxStore(pool)
	d, found, err := store.Claim(ctx, "delivery")
	if err != nil || !found {
		t.Fatal(err)
	}
	if err := store.Published(ctx, d); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../migrations/000010_outbox_publication.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(data)); err == nil {
		t.Fatal("down removeu histórico de entrega")
	}
	_, _ = pool.Exec(ctx, "ROLLBACK")
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
}
