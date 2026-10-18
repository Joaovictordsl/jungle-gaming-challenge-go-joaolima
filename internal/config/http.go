package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// HTTP mantém a configuração do transporte separada da conexão com o banco.
type HTTP struct {
	Address         string
	ShutdownTimeout time.Duration
}

func LoadHTTP() (HTTP, error) {
	cfg := HTTP{Address: "127.0.0.1:8080", ShutdownTimeout: 5 * time.Second}
	if address, exists := os.LookupEnv("HTTP_ADDR"); exists {
		cfg.Address = address
	}
	if raw, exists := os.LookupEnv("HTTP_SHUTDOWN_TIMEOUT"); exists {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return HTTP{}, fmt.Errorf("%w: HTTP_SHUTDOWN_TIMEOUT deve ser uma duração positiva", ErrInvalidConfig)
		}
		cfg.ShutdownTimeout = timeout
	}
	if err := cfg.Validate(); err != nil {
		return HTTP{}, err
	}
	return cfg, nil
}

func (c HTTP) Validate() error {
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("%w: HTTP_ADDR deve ter formato host:porta", ErrInvalidConfig)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("%w: porta de HTTP_ADDR deve estar entre 0 e 65535", ErrInvalidConfig)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("%w: HTTP_SHUTDOWN_TIMEOUT deve ser positiva", ErrInvalidConfig)
	}
	return nil
}
