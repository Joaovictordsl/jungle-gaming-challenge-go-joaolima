package wallet

import (
	"context"
	"time"

	domainwallet "jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

// OpeningStore persiste uma abertura completa de forma atômica.
type OpeningStore interface {
	// SaveOpening salva a carteira e, quando o saldo inicial é positivo,
	// a transação OPENING em PROCESSED, o crédito no ledger e os eventos
	// WagerTransactionProcessed e WalletBalanceChanged na outbox.
	// Todos esses registros devem compartilhar a mesma transação SQL.
	// Saldo zero salva apenas a carteira. A abertura mantém a versão 1.
	// Um conflito de jogador e moeda deve preservar domainwallet.ErrAlreadyExists.
	// A operação deve respeitar o contexto e reverter tudo em caso de erro
	// anterior ao commit. Os eventos são apenas registrados, sem publicação.
	SaveOpening(ctx context.Context, w domainwallet.Wallet) error
}

// IDGenerator fornece identificadores para novas carteiras.
type IDGenerator interface {
	NewID() (string, error)
}

// Clock fornece o instante usado na abertura e permite testes determinísticos.
type Clock interface {
	Now() time.Time
}
