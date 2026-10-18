package wallet

import "errors"

var (
	ErrInvalidID       = errors.New("identificador da carteira inválido")
	ErrInvalidPlayerID = errors.New("identificador do jogador inválido")
	ErrNegativeBalance = errors.New("saldo da carteira não pode ser negativo")
	ErrInvalidVersion  = errors.New("versão da carteira inválida")
	ErrInvalidTime     = errors.New("instantes da carteira inválidos")
	ErrAlreadyExists   = errors.New("carteira já existe para o identificador ou jogador e moeda")
	ErrNotFound        = errors.New("carteira não encontrada")
	ErrNotInitialState = errors.New("carteira não está no estado inicial")
	ErrUnavailable     = errors.New("persistência de carteiras temporariamente indisponível")
)
