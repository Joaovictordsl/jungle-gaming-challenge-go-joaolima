package app

import (
	"crypto/rand"
	"time"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
)

type walletIDs struct{}

func (walletIDs) NewID() (string, error) { return rand.Text(), nil }

type walletClock struct{}

func (walletClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func newWalletIDs() applicationwallet.IDGenerator { return walletIDs{} }
func newWalletClock() applicationwallet.Clock     { return walletClock{} }
