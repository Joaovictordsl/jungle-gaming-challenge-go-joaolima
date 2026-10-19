package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
)

func httpTestApp(t *testing.T, cfg config.HTTP) (*fx.App, *Server, *gin.Engine) {
	t.Helper()
	var server *Server
	var router *gin.Engine
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	application := fx.New(Module, fx.Supply(logger), fx.Replace(cfg),
		fx.Supply(newHandlerTestOpener(handlerStoreFunc(func(context.Context, wallet.Wallet) error { return nil }))),
		fx.Supply(testBetProcessor(func(context.Context, wager.Bet) (wager.Bet, bool, error) {
			return wager.Bet{}, false, errors.New("apostas não participam do teste de lifecycle")
		})),
		fx.Supply(testWinProcessor(func(context.Context, wager.Win) (wager.Win, bool, error) {
			return wager.Win{}, false, errors.New("WIN não participa do teste de lifecycle")
		})),
		fx.Supply(unusedLossProcessor()),
		fx.Supply(unusedRefundProcessor()),
		fx.Supply(unusedRollbackProcessor()),
		fx.Supply(newTestQueries(), metrics.NewReconciliation()),
		fx.Supply(fx.Annotate(handlerRepository{}, fx.As(new(wallet.Repository)))),
		fx.Replace(fx.Annotate(tokenVerifierFunc(func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{}, auth.ErrInvalidToken
		}), fx.As(new(auth.TokenVerifier)))),
		fx.Populate(&server, &router), fx.NopLogger)
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := application.Stop(ctx); err != nil {
			t.Errorf("Erro ao encerrar aplicação de teste: %v", err)
		}
	})
	return application, server, router
}

func testHTTPClient(t *testing.T) *nethttp.Client {
	t.Helper()
	transport := &nethttp.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return &nethttp.Client{Timeout: 3 * time.Second, Transport: transport}
}

func startHTTPTestApp(t *testing.T, application *fx.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestServerLifecycle(t *testing.T) {
	application, server, _ := httpTestApp(t, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: time.Second})
	startHTTPTestApp(t, application)
	client := testHTTPClient(t)
	response, err := client.Get("http://" + server.Address() + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != nethttp.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("Servidor real não respondeu ao contrato: %s, %v", body, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get("http://" + server.Address() + "/health/live"); err == nil {
		response.Body.Close()
		t.Fatal("Servidor não deve aceitar conexões após Stop")
	}
}

func TestServerRejectsOccupiedPortDuringStartup(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	application, _, _ := httpTestApp(t, config.HTTP{Address: occupied.Addr().String(), ShutdownTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Start(ctx); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Porta ocupada deve falhar na inicialização: %v", err)
	}
}

func TestServerShutdownWaitsForActiveRequest(t *testing.T) {
	application, server, router := httpTestApp(t, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second})
	started, release, shuttingDown := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	router.GET("/test-wait", func(c *gin.Context) {
		close(started)
		select {
		case <-release:
			c.JSON(nethttp.StatusOK, gin.H{"finished": true})
		case <-c.Request.Context().Done():
		}
	})
	server.server.RegisterOnShutdown(func() { close(shuttingDown) })
	startHTTPTestApp(t, application)
	client := testHTTPClient(t)
	requestDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + server.Address() + "/test-wait")
		if err == nil {
			_, err = io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != nethttp.StatusOK {
				err = errors.New("requisição ativa não foi concluída com sucesso")
			}
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Requisição não chegou ao servidor")
	}
	stopDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stopDone <- application.Stop(ctx)
	}()
	select {
	case <-shuttingDown:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown não iniciou")
	}
	select {
	case err := <-stopDone:
		t.Fatalf("Shutdown terminou antes da requisição ativa: %v", err)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
}

func TestServerShutdownTimeoutClosesRemainingConnections(t *testing.T) {
	application, server, router := httpTestApp(t, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: 50 * time.Millisecond})
	started, canceled := make(chan struct{}), make(chan struct{})
	router.GET("/test-blocked", func(c *gin.Context) {
		close(started)
		<-c.Request.Context().Done()
		close(canceled)
	})
	startHTTPTestApp(t, application)
	client := testHTTPClient(t)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		if response, err := client.Get("http://" + server.Address() + "/test-blocked"); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Requisição não chegou ao servidor")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown deve preservar o erro de prazo esgotado: %v", err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("Conexão restante não foi fechada")
	}
	<-requestDone
}

func TestUnexpectedServeFailureRequestsApplicationShutdown(t *testing.T) {
	application, server, _ := httpTestApp(t, config.HTTP{Address: "127.0.0.1:0", ShutdownTimeout: time.Second})
	startHTTPTestApp(t, application)
	shutdown := application.Wait()
	server.mu.RLock()
	listener := server.listener
	server.mu.RUnlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case signal := <-shutdown:
		if signal.ExitCode != 1 {
			t.Fatalf("Falha inesperada deve solicitar código de saída 1: %d", signal.ExitCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Falha do listener deve solicitar encerramento pelo Fx")
	}
}
