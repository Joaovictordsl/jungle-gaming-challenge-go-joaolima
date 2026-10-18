package wallet

import "context"

type Repository interface {
	Create(ctx context.Context, w Wallet) error
	FindByID(ctx context.Context, id string) (Wallet, error)
}
