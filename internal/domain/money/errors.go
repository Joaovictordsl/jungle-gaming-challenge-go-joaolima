package money

import "errors"

var (
	ErrInvalidAmount    = errors.New("valor monetário inválido")
	ErrInvalidCurrency  = errors.New("moeda não suportada")
	ErrCurrencyMismatch = errors.New("moedas incompatíveis")
	ErrUninitialized    = errors.New("valor monetário não inicializado")
	ErrOverflow         = errors.New("valor fora dos limites de int64")
)
