package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/outbox"
)

type OutboxStore struct{ pool *pgxpool.Pool }

func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore { return &OutboxStore{pool: pool} }
func (s *OutboxStore) Claim(ctx context.Context, token string) (outbox.Delivery, bool, error) {
	var d outbox.Delivery
	if strings.TrimSpace(token) == "" || len(token) > 256 {
		return d, false, errors.New("token de publicação inválido")
	}
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT e.id,e.locked_until IS NOT NULL AS recovered FROM outbox_events e
 WHERE e.published_at IS NULL AND e.next_attempt_at<=clock_timestamp()
 AND (e.locked_until IS NULL OR e.locked_until<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM outbox_events prior WHERE prior.aggregate_id=e.aggregate_id
  AND prior.published_at IS NULL AND prior.publication_sequence<e.publication_sequence)
 ORDER BY e.next_attempt_at,e.publication_sequence FOR UPDATE OF e SKIP LOCKED LIMIT 1
 ) UPDATE outbox_events e SET locked_by=$1,locked_until=clock_timestamp()+$2::bigint*interval '1 microsecond',
 attempts=LEAST(e.attempts::bigint+1,2147483647)::integer FROM candidate c WHERE e.id=c.id
 RETURNING e.id,e.aggregate_id,e.transaction_id,COALESCE(e.payload->'data'->>'providerId',''),e.event_type,e.payload,e.publication_sequence,e.attempts,e.locked_by,c.recovered`, token, outbox.Lease.Microseconds()).Scan(&d.ID, &d.AggregateID, &d.TransactionID, &d.ProviderID, &d.EventType, &d.Payload, &d.Sequence, &d.Attempts, &d.Token, &d.Recovered)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}
func (s *OutboxStore) Published(ctx context.Context, d outbox.Delivery) error {
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at=clock_timestamp(),locked_by=NULL,locked_until=NULL
 WHERE id=$1 AND locked_by=$2 AND published_at IS NULL AND locked_until>clock_timestamp()`, d.ID, d.Token)
	if err == nil && tag.RowsAffected() != 1 {
		return outbox.ErrLeaseLost
	}
	return err
}
func (s *OutboxStore) Retry(ctx context.Context, d outbox.Delivery, delay time.Duration) error {
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=clock_timestamp()+$3::bigint*interval '1 microsecond',locked_by=NULL,locked_until=NULL
 WHERE id=$1 AND locked_by=$2 AND published_at IS NULL AND locked_until>clock_timestamp()`, d.ID, d.Token, delay.Microseconds())
	if err == nil && tag.RowsAffected() != 1 {
		return outbox.ErrLeaseLost
	}
	return err
}
func (s *OutboxStore) Release(ctx context.Context, d outbox.Delivery) error {
	_, err := s.pool.Exec(ctx, `UPDATE outbox_events SET locked_by=NULL,locked_until=NULL WHERE id=$1 AND locked_by=$2 AND published_at IS NULL`, d.ID, d.Token)
	return err
}
func (s *OutboxStore) Backlog(ctx context.Context) (outbox.Backlog, error) {
	var b outbox.Backlog
	err := s.pool.QueryRow(ctx, `SELECT count(*),min(occurred_at) FROM outbox_events WHERE published_at IS NULL`).Scan(&b.Pending, &b.Oldest)
	return b, err
}
