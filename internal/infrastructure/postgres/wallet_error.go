package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

// Classifica falhas transitórias sem obrigar o transporte HTTP a conhecer pgx.
func walletPersistenceError(operation string, err error) error {
	var networkError net.Error
	var postgresError *pgconn.PgError
	temporary := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError)
	if errors.As(err, &postgresError) {
		code := postgresError.Code
		temporary = temporary || strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") ||
			code == "40001" || code == "40P01" || code == "57P01" || code == "57P02" || code == "57P03"
	}
	if temporary {
		return fmt.Errorf("%s: %w: %w", operation, wallet.ErrUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
