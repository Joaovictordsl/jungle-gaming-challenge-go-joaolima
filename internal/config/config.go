package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidConfig = errors.New("configuração inválida")

type Config struct {
	DatabaseURL            string
	DatabaseConnectTimeout time.Duration
	DatabaseMaxConns       int32
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		DatabaseConnectTimeout: 5 * time.Second,
		DatabaseMaxConns:       10,
	}
	if raw, exists := os.LookupEnv("DATABASE_CONNECT_TIMEOUT"); exists {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%w: DATABASE_CONNECT_TIMEOUT deve ser uma duração positiva", ErrInvalidConfig)
		}
		cfg.DatabaseConnectTimeout = timeout
	}
	if raw, exists := os.LookupEnv("DATABASE_MAX_CONNS"); exists {
		maxConns, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return Config{}, fmt.Errorf("%w: DATABASE_MAX_CONNS deve ser um inteiro positivo", ErrInvalidConfig)
		}
		cfg.DatabaseMaxConns = int32(maxConns)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("%w: DATABASE_URL é obrigatória", ErrInvalidConfig)
	}
	if c.DatabaseConnectTimeout <= 0 {
		return fmt.Errorf("%w: DATABASE_CONNECT_TIMEOUT deve ser positiva", ErrInvalidConfig)
	}
	if c.DatabaseMaxConns <= 0 {
		return fmt.Errorf("%w: DATABASE_MAX_CONNS deve ser maior que zero", ErrInvalidConfig)
	}
	return nil
}
