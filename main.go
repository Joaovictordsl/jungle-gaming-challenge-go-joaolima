package main

import (
	"fmt"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func main() {

	valor, err := money.New("35.05", "USD")
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(valor.MinorUnits())
	fmt.Println(valor.Currency())
}
