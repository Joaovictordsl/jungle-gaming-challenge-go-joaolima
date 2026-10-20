//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

type inboxIDs struct{}

func (inboxIDs) NewID() (string, error) { return rand.Text(), nil }

type inboxClock struct{}

func (inboxClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
func inboxTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := rollbackTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000008_ledger_queries.up.sql")
	runOpeningMigration(t, ctx, pool, "000009_inbox.up.sql")
	return ctx, pool
}
func inboxService(pool *pgxpool.Pool) *message.Service {
	return message.NewService(NewInboxStore(pool), appwager.NewDispatcher(inboxIDs{}, inboxClock{}))
}
func inboxEnvelope(t *testing.T, w wallet.Wallet, id, external, key, amount string) message.Envelope {
	t.Helper()
	bet := pendingTestBet(t, w, external, key, amount)
	return message.Envelope{MessageID: id, OccurredAt: time.Now().UTC().Truncate(time.Microsecond), Command: appwager.Command{BetRequest: bet.Request(), Kind: "BET"}}
}
func countInbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_messages`).Scan(&count); err != nil || count != want {
		t.Fatal("Inbox inesperada", count, err)
	}
}

func TestInboxAndHTTPShareFinancialIdempotencyIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "inbox-http", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	httpBet, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "key", "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg-1", "bet", "key", "00080.00")
	first, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false)
	if err != nil || replay || !first.FinancialReplay || first.TransactionID != httpBet.ID() || first.Balance.MinorUnits() != 2000 {
		t.Fatal("SQS reaplicou operação HTTP", err)
	}
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "win", "win-key", "10.00", "")); err != nil {
		t.Fatal(err)
	}
	again, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false)
	if err != nil || !replay || again != first {
		t.Fatal("Replay da inbox alterou resultado", err)
	}
	other := e
	other.MessageID = "msg-2"
	if r, replay, err := inboxService(pool).Execute(ctx, "consumer", other, false); err != nil || replay || !r.FinancialReplay || r.TransactionID != first.TransactionID {
		t.Fatal(err)
	}
	changed := e
	changed.Command.RoundID = "other"
	if _, _, err := inboxService(pool).Execute(ctx, "consumer", changed, false); !errors.Is(err, message.ErrInboxConflict) {
		t.Fatal("Mesmo messageId com outro conteúdo aceito", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 3, 6})
	countInbox(t, ctx, pool, 2)
}

func TestInboxConcurrentCopiesCommitOnceIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "inbox-concurrent", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "bet", "key", "80.00")
	start := make(chan struct{})
	failures := make(chan error, 30)
	replays := make(chan bool, 30)
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			<-start
			_, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false)
			failures <- err
			replays <- replay
		})
	}
	close(start)
	wg.Wait()
	close(failures)
	close(replays)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	originals := 0
	for replay := range replays {
		if !replay {
			originals++
		}
	}
	if originals != 1 {
		t.Fatal("Inbox confirmada mais de uma vez")
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
	countInbox(t, ctx, pool, 1)
}

func TestInboxRollsBackFinancialAndAuditOnTransientFailureIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000010_outbox_publication.up.sql")
	runOpeningMigration(t, ctx, pool, "000011_financial_failed.up.sql")
	w := repositoryTestWallet(t, "inbox-atomic", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "bet", "key", "80.00")
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT fail_inbox_event CHECK(event_type<>'WalletBalanceChanged') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inboxService(pool).Execute(ctx, "consumer", e, false); err == nil {
		t.Fatal("Falha da outbox ignorada")
	}
	countInbox(t, ctx, pool, 0)
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
	if _, _, err := inboxService(pool).Execute(ctx, "consumer", e, true); message.PermanentCode(err) != "attempts_exhausted" {
		t.Fatal("Última falha não auditada", err)
	}
	countInbox(t, ctx, pool, 1)
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 1, 2})
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT fail_inbox_event`); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false); !replay || message.PermanentCode(err) != "attempts_exhausted" {
		t.Fatal("Falha terminal foi reaplicada", err)
	}
	fresh := e
	fresh.MessageID = "new-msg"
	if result, _, err := inboxService(pool).Execute(ctx, "consumer", fresh, false); message.PermanentCode(err) != "attempts_exhausted" || result.Status != "FAILED" || !result.FinancialReplay || result.Balance.MinorUnits() != 10000 {
		t.Fatal("Falha terminal perdeu identidade financeira", err)
	}
}

func TestInboxCommitFailureDoesNotLeaveFinancialEffectsIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "inbox-commit", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_inbox_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected' USING ERRCODE='23514'; END; $$; CREATE CONSTRAINT TRIGGER injected_inbox_commit AFTER INSERT ON inbox_messages DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_inbox_commit()`); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "bet", "key", "80.00")
	if _, _, err := inboxService(pool).Execute(ctx, "consumer", e, false); err == nil {
		t.Fatal("Commit inválido aceito")
	}
	countInbox(t, ctx, pool, 0)
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
	if _, err := pool.Exec(ctx, `DROP TRIGGER injected_inbox_commit ON inbox_messages`); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false); err != nil || replay {
		t.Fatal("Retry não concluiu", err)
	}
	countInbox(t, ctx, pool, 1)
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
}

func TestInboxPendingReferenceAndPermanentFailureIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "inbox-pending", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "refund", "key", "80.00")
	e.Command.Kind = "REFUND"
	e.Command.ReferenceExternalTransactionID = "future-bet"
	first, _, err := inboxService(pool).Execute(ctx, "consumer", e, false)
	if err != nil || first.Status != "PENDING_REFERENCE" {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "future-bet", "bet-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	refs := NewWinStore(pool)
	refs.now = func() time.Time { return e.OccurredAt.Add(2 * time.Second) }
	if worked, err := refs.RetryDueReference(ctx); err != nil || !worked {
		t.Fatal(err)
	}
	repeated, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false)
	if err != nil || !replay || repeated != first {
		t.Fatal("Inbox perdeu snapshot original", err)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 10000 || loaded.Version() != 3 {
		t.Fatal(err)
	}
	missing := inboxEnvelope(t, w, "missing", "missing", "missing-key", "1.00")
	missing.Command.WalletID = "absent"
	if _, _, err := inboxService(pool).Execute(ctx, "consumer", missing, false); message.PermanentCode(err) != "wallet_not_found" {
		t.Fatal(err)
	}
	countInbox(t, ctx, pool, 2)
	for _, statement := range []string{`DELETE FROM inbox_messages`, `UPDATE inbox_messages SET status='PENDING'`, `TRUNCATE inbox_messages`} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatal("Banco permitiu alterar inbox terminal")
		}
	}
}

func TestInboxAcrossThreeIndependentProcessesIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "distributed-inbox", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	inputs := []betProcessInput{
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet-a", Key: "key-a"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet-b", Key: "key-b", MessageID: "msg-b"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet-a", Key: "key-a", MessageID: "msg-a"},
	}
	results := runBetProcesses(t, ctx, pool, inputs, func() {
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	})
	unique := map[string]betProcessOutput{}
	replays := 0
	for _, r := range results {
		unique[r.TransactionID] = r
		if r.Replay {
			replays++
		}
	}
	processed, rejected := 0, 0
	for _, r := range unique {
		switch r.Status {
		case "PROCESSED":
			processed++
		case "REJECTED":
			rejected++
		}
	}
	if len(unique) != 2 || replays != 1 || processed != 1 || rejected != 1 {
		t.Fatalf("concorrência entre entrada direta e inbox: %+v", results)
	}
	for _, r := range runBetProcesses(t, ctx, pool, inputs, nil) {
		original, ok := unique[r.TransactionID]
		if !ok || !r.Replay || r.Status != original.Status || r.Balance != original.Balance {
			t.Fatal("replay em processo novo mudou resultado")
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 2, 5})
	countInbox(t, ctx, pool, 2)
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 2000 || loaded.Version() != 2 {
		t.Fatal("saldo ou versão incorretos", err)
	}
}

func TestInboxMigrationAndUnfinishedCommitIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "migration-inbox", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	runOpeningMigration(t, ctx, pool, "000009_inbox.down.sql")
	runOpeningMigration(t, ctx, pool, "000009_inbox.up.sql")
	_, err := pool.Exec(ctx, `INSERT INTO inbox_messages(consumer_name,message_id,payload_hash,provider_id,external_transaction_id,wallet_id,occurred_at,received_at,status) VALUES('consumer','unfinished',repeat('a',64),'provider-a','ext','migration-inbox',now(),now(),'PENDING')`)
	if err == nil {
		t.Fatal("PENDING incompleto chegou ao commit")
	}
	countInbox(t, ctx, pool, 0)
	e := inboxEnvelope(t, w, "msg", "bet", "key", "80.00")
	if _, _, err := inboxService(pool).Execute(ctx, "consumer", e, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../migrations/000009_inbox.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(data)); err == nil {
		t.Fatal("down removeu histórico de inbox")
	}
	// O script down usa BEGIN: retire a conexão abortada explicitamente.
	if _, err := pool.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	countInbox(t, ctx, pool, 1)
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
}

func TestInboxCancellationReleasesClaimAndWalletIntegration(t *testing.T) {
	ctx, pool := inboxTestDatabase(t)
	w := repositoryTestWallet(t, "cancel-inbox", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "bet", "key", "80.00")
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, _, err := inboxService(pool).Execute(short, "consumer", e, false); err == nil {
		t.Fatal("cancelamento ignorado")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false); err != nil || replay {
		t.Fatal("claim ficou presa após cancelamento", err)
	}
	countInbox(t, ctx, pool, 1)
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
}
