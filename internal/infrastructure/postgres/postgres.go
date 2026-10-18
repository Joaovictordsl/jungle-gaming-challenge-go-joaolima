package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

var (
	ErrInvalidConfig = errors.New("configuração do PostgreSQL inválida")
	ErrUnavailable   = errors.New("PostgreSQL indisponível: verifique endereço, credenciais e disponibilidade")
)

var Module = fx.Module("postgres",
	fx.Provide(NewPool),
	fx.Provide(func(pool *pgxpool.Pool) wallet.Repository {
		return NewWalletRepository(pool)
	}),
	fx.Provide(func(pool *pgxpool.Pool) applicationwallet.OpeningStore {
		return NewOpeningStore(pool)
	}),
	fx.Invoke(func(*pgxpool.Pool) {}),
)

func NewPool(lifecycle fx.Lifecycle, cfg config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		// O erro do parser pode conter a URL completa, incluindo a senha.
		return nil, ErrInvalidConfig
	}
	poolConfig.MaxConns = cfg.DatabaseMaxConns
	poolConfig.MinConns = 0
	poolConfig.MinIdleConns = 0
	poolConfig.ConnConfig.ConnectTimeout = cfg.DatabaseConnectTimeout

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, cfg.DatabaseConnectTimeout)
			defer cancel()
			if err := pool.Ping(ctx); err != nil {
				// Fx não chama OnStop do hook cujo OnStart falhou.
				pool.Close()
				return connectionError(err)
			}
			logger.Info("Conexão com PostgreSQL validada", "maxConnections", cfg.DatabaseMaxConns)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			logger.Info("Pool do PostgreSQL encerrado")
			return nil
		},
	})
	return pool, nil
}

func connectionError(err error) error {
	// Preserva cancelamento e timeout sem propagar credenciais em mensagens do driver.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrUnavailable, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrUnavailable, context.DeadlineExceeded)
	}
	return ErrUnavailable
}
