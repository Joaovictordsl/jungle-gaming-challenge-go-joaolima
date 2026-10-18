package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwallet "jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

type openingStoreFunc func(context.Context, domainwallet.Wallet) error

func (f openingStoreFunc) SaveOpening(ctx context.Context, w domainwallet.Wallet) error {
	return f(ctx, w)
}

type idGeneratorFunc func() (string, error)

func (f idGeneratorFunc) NewID() (string, error) { return f() }

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

func openingTestBalance(t *testing.T, amount string) money.Money {
	t.Helper()
	balance, err := money.New(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return balance
}

func TestOpenWalletExecutePersistsInitialState(t *testing.T) {
	for _, amount := range []string{"0.00", "100.25", "92233720368547758.07"} {
		t.Run(amount, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Date(2026, time.October, 18, 10, 30, 0, 0, time.FixedZone("BRT", -3*60*60))
			balance := openingTestBalance(t, amount)
			var saved domainwallet.Wallet
			var saves, generatedIDs int
			store := openingStoreFunc(func(gotCtx context.Context, w domainwallet.Wallet) error {
				if gotCtx != ctx {
					t.Fatal("Persistência deve receber o contexto da chamada")
				}
				saves++
				saved = w
				return nil
			})
			uc := NewOpenWallet(store, idGeneratorFunc(func() (string, error) {
				generatedIDs++
				return "wallet-1", nil
			}), clockFunc(func() time.Time { return now }))

			got, err := uc.Execute(ctx, OpenWalletInput{PlayerID: "player-1", InitialBalance: balance})
			if err != nil {
				t.Fatal(err)
			}
			if saves != 1 || generatedIDs != 1 || got != saved {
				t.Fatal("Abertura deve gerar um ID e persistir exatamente a carteira retornada uma vez")
			}
			if got.ID() != "wallet-1" || got.PlayerID() != "player-1" || got.Balance() != balance || got.Version() != 1 {
				t.Fatal("Estado inicial da carteira foi alterado")
			}
			if !got.CreatedAt().Equal(now) || !got.UpdatedAt().Equal(now) || got.CreatedAt().Location() != time.UTC {
				t.Fatal("Abertura deve usar o relógio injetado e representar o instante em UTC")
			}
		})
	}
}

func TestOpenWalletExecuteRejectsCanceledContextBeforeDependencies(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Time{})
	defer cancelExpired()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{canceled, expired} {
		uc := NewOpenWallet(
			openingStoreFunc(func(context.Context, domainwallet.Wallet) error {
				t.Fatal("Contexto encerrado não deve chamar a persistência")
				return nil
			}),
			idGeneratorFunc(func() (string, error) {
				t.Fatal("Contexto encerrado não deve gerar ID")
				return "", nil
			}),
			clockFunc(func() time.Time {
				t.Fatal("Contexto encerrado não deve consultar o relógio")
				return time.Time{}
			}),
		)
		got, err := uc.Execute(ctx, OpenWalletInput{})
		if !errors.Is(err, ctx.Err()) || got != (domainwallet.Wallet{}) {
			t.Fatalf("Esperava carteira vazia e erro de contexto %v; recebeu %v", ctx.Err(), err)
		}
	}
}

func TestOpenWalletExecutePreservesIDGenerationError(t *testing.T) {
	cause := errors.New("gerador indisponível")
	uc := NewOpenWallet(
		openingStoreFunc(func(context.Context, domainwallet.Wallet) error {
			t.Fatal("Falha na geração do ID não deve persistir")
			return nil
		}),
		idGeneratorFunc(func() (string, error) { return "", cause }),
		clockFunc(func() time.Time {
			t.Fatal("Falha na geração do ID não deve consultar o relógio")
			return time.Time{}
		}),
	)
	got, err := uc.Execute(context.Background(), OpenWalletInput{})
	if !errors.Is(err, cause) || got != (domainwallet.Wallet{}) {
		t.Fatalf("Esperava carteira vazia e causa preservada; recebeu %v", err)
	}
}

func TestOpenWalletExecuteRejectsInvalidWalletBeforePersistence(t *testing.T) {
	balance := openingTestBalance(t, "10.00")
	now := time.Date(2026, time.October, 18, 13, 30, 0, 0, time.UTC)
	cases := []struct {
		name  string
		id    string
		input OpenWalletInput
		now   time.Time
		want  error
	}{
		{name: "jogador inválido", id: "wallet-1", input: OpenWalletInput{InitialBalance: balance}, now: now, want: domainwallet.ErrInvalidPlayerID},
		{name: "saldo não inicializado", id: "wallet-1", input: OpenWalletInput{PlayerID: "player-1"}, now: now, want: money.ErrUninitialized},
		{name: "ID gerado inválido", input: OpenWalletInput{PlayerID: "player-1", InitialBalance: balance}, now: now, want: domainwallet.ErrInvalidID},
		{name: "relógio inválido", id: "wallet-1", input: OpenWalletInput{PlayerID: "player-1", InitialBalance: balance}, want: domainwallet.ErrInvalidTime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc := NewOpenWallet(
				openingStoreFunc(func(context.Context, domainwallet.Wallet) error {
					t.Fatal("Carteira inválida não deve chegar à persistência")
					return nil
				}),
				idGeneratorFunc(func() (string, error) { return tc.id, nil }),
				clockFunc(func() time.Time { return tc.now }),
			)
			got, err := uc.Execute(context.Background(), tc.input)
			if !errors.Is(err, tc.want) || got != (domainwallet.Wallet{}) {
				t.Fatalf("Esperava carteira vazia e %v; recebeu %v", tc.want, err)
			}
		})
	}
}

func TestOpenWalletExecutePreservesPersistenceError(t *testing.T) {
	for _, cause := range []error{domainwallet.ErrAlreadyExists, context.Canceled, context.DeadlineExceeded, errors.New("falha no commit")} {
		t.Run(cause.Error(), func(t *testing.T) {
			saves := 0
			uc := NewOpenWallet(
				openingStoreFunc(func(context.Context, domainwallet.Wallet) error {
					saves++
					return cause
				}),
				idGeneratorFunc(func() (string, error) { return "wallet-1", nil }),
				clockFunc(func() time.Time { return time.Date(2026, time.October, 18, 13, 30, 0, 0, time.UTC) }),
			)
			got, err := uc.Execute(context.Background(), OpenWalletInput{PlayerID: "player-1", InitialBalance: openingTestBalance(t, "10.00")})
			if !errors.Is(err, cause) || got != (domainwallet.Wallet{}) || saves != 1 {
				t.Fatalf("Erro de persistência deve ser preservado sem repetir a abertura; recebeu %v", err)
			}
		})
	}
}

func TestOpenWalletExecuteDoesNotPersistAfterCancellationDuringPreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uc := NewOpenWallet(
		openingStoreFunc(func(context.Context, domainwallet.Wallet) error {
			t.Fatal("Cancelamento anterior à persistência deve interromper a abertura")
			return nil
		}),
		idGeneratorFunc(func() (string, error) {
			cancel()
			return "wallet-1", nil
		}),
		clockFunc(func() time.Time { return time.Date(2026, time.October, 18, 13, 30, 0, 0, time.UTC) }),
	)
	got, err := uc.Execute(ctx, OpenWalletInput{PlayerID: "player-1", InitialBalance: openingTestBalance(t, "0.00")})
	if !errors.Is(err, context.Canceled) || got != (domainwallet.Wallet{}) {
		t.Fatalf("Esperava carteira vazia e cancelamento; recebeu %v", err)
	}
}

func TestOpenWalletExecuteReturnsSuccessAfterConfirmedPersistence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uc := NewOpenWallet(
		openingStoreFunc(func(context.Context, domainwallet.Wallet) error {
			// Simula cancelamento depois que a persistência confirmou a abertura.
			cancel()
			return nil
		}),
		idGeneratorFunc(func() (string, error) { return "wallet-1", nil }),
		clockFunc(func() time.Time { return time.Date(2026, time.October, 18, 13, 30, 0, 0, time.UTC) }),
	)
	got, err := uc.Execute(ctx, OpenWalletInput{PlayerID: "player-1", InitialBalance: openingTestBalance(t, "10.00")})
	if err != nil || got.ID() != "wallet-1" {
		t.Fatalf("Confirmação bem-sucedida não deve ser reportada como cancelamento: %v", err)
	}
}
