package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	nethttp "net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
)

var Module = fx.Module("http",
	auth.Module,
	fx.Provide(config.LoadHTTP, NewRouter, NewServer, NewAuthentication, NewWalletHandler, NewBetHandler, NewQueryHandler),
	fx.Invoke(RegisterWalletRoutes),
	fx.Invoke(RegisterBetRoutes),
	fx.Invoke(RegisterQueryRoutes),
	fx.Invoke(func(*Authentication, *Server) {}),
)

type Server struct {
	server   *nethttp.Server
	mu       sync.RWMutex
	listener net.Listener
}

func NewServer(lifecycle fx.Lifecycle, cfg config.HTTP, router *gin.Engine, logger *slog.Logger, shutdowner fx.Shutdowner) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Server{server: &nethttp.Server{
		Addr: cfg.Address, Handler: router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var listenConfig net.ListenConfig
			listener, err := listenConfig.Listen(ctx, "tcp", cfg.Address)
			if err != nil {
				return fmt.Errorf("iniciar servidor HTTP: %w", err)
			}
			s.mu.Lock()
			s.listener = listener
			s.mu.Unlock()
			go func() {
				if err := s.server.Serve(listener); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
					logger.Error("Servidor HTTP interrompido", "error", err)
					if err := shutdowner.Shutdown(fx.ExitCode(1)); err != nil {
						logger.Error("Falha ao solicitar encerramento da aplicação", "error", err)
					}
				}
			}()
			logger.Info("Servidor HTTP iniciado", "address", listener.Addr().String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()
			if err := s.server.Shutdown(shutdownCtx); err != nil {
				// O prazo acabou: fecha as conexões restantes e preserva a causa.
				return errors.Join(err, s.server.Close())
			}
			logger.Info("Servidor HTTP encerrado")
			return nil
		},
	})
	return s, nil
}

// Address devolve o endereço efetivamente aberto, inclusive quando a porta é 0.
func (s *Server) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}
