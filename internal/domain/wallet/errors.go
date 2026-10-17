package wallet

import "errors"

var (
	ErrInvalidID       = errors.New("identificador da carteira inválido")
	ErrInvalidPlayerID = errors.New("identificador do jogador inválido")
	ErrNegativeBalance = errors.New("saldo da carteira não pode ser negativo")
	ErrInvalidTime     = errors.New("instante de criação inválido")
)
