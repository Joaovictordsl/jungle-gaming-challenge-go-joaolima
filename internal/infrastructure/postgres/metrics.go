package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
)

func MetricsSnapshot(pool *pgxpool.Pool) metrics.Probe {
	return func(ctx context.Context) (string, error) {
		rows, err := pool.Query(ctx, `SELECT kind,status,count(*),COALESCE(sum(reference_attempts),0)::bigint FROM wager_transactions GROUP BY kind,status`)
		if err != nil {
			return "", err
		}
		var counts []metrics.TransactionCount
		for rows.Next() {
			var row metrics.TransactionCount
			if err := rows.Scan(&row.Kind, &row.Status, &row.Count, &row.ReferenceAttempts); err != nil {
				rows.Close()
				return "", err
			}
			counts = append(counts, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		backlog, err := NewOutboxStore(pool).Backlog(ctx)
		if err != nil {
			return "", err
		}
		age := time.Duration(0)
		if backlog.Oldest != nil {
			age = max(0, time.Since(*backlog.Oldest))
		}
		return metrics.RenderTransactions(counts) + fmt.Sprintf("# TYPE jungle_outbox_unpublished gauge\njungle_outbox_unpublished %d\n# TYPE jungle_outbox_lag_seconds gauge\njungle_outbox_lag_seconds %g\n", backlog.Pending, age.Seconds()), nil
	}
}
