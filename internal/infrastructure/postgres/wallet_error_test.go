package postgres

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func TestWalletPersistenceErrorPreservesCauseAndClassifiesAvailability(t *testing.T) {
	for _, tc := range []struct {
		cause       error
		unavailable bool
	}{
		{context.Canceled, true}, {context.DeadlineExceeded, true},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{&pgconn.PgError{Code: "08006"}, true}, {&pgconn.PgError{Code: "40001"}, true},
		{&pgconn.PgError{Code: "40P01"}, true}, {&pgconn.PgError{Code: "53300"}, true}, {&pgconn.PgError{Code: "57P01"}, true},
		{&pgconn.PgError{Code: "23505"}, false}, {&pgconn.PgError{Code: "23514"}, false}, {errors.New("unknown failure"), false},
	} {
		err := walletPersistenceError("test", tc.cause)
		if !errors.Is(err, tc.cause) || errors.Is(err, wallet.ErrUnavailable) != tc.unavailable {
			t.Fatalf("Classificação incorreta para %T", tc.cause)
		}
	}
}
