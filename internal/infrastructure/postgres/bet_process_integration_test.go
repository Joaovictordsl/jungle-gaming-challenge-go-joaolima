//go:build integration

package postgres

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type betProcessInput struct{ WalletID, PlayerID, External, Key, Kind, Amount, Reference string }
type betProcessOutput struct {
	TransactionID, Status string
	Balance               int64
	Replay                bool
}

// Cada filho é um processo do sistema operacional, com memória e pool próprios.
func TestBetIndependentProcessHelper(t *testing.T) {
	if os.Getenv("JUNGLE_BET_CHILD") != "1" {
		t.Skip("Executado apenas pelo teste com três processos")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("JUNGLE_BET_DATABASE_URL"))
	if err != nil {
		t.Fatal("Banco do processo inválido")
	}
	defer pool.Close()
	var input betProcessInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		t.Fatal(err)
	}
	if input.Amount == "" {
		input.Amount = "80.00"
	}
	amount, err := money.New(input.Amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	request := wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: input.External, IdempotencyKey: input.Key, WalletID: input.WalletID, PlayerID: input.PlayerID, RoundID: "round", GameID: "game", Money: amount}
	if input.Kind == wager.LossKind {
		pending, err := wager.NewLoss(rand.Text(), wager.LossRequest{BetRequest: request}, time.Now().UTC().Truncate(time.Microsecond))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("BET_READY")
		result, replay, err := NewLossStore(pool).ProcessLoss(ctx, pending)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(betProcessOutput{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance().MinorUnits(), Replay: replay})
		fmt.Println("BET_RESULT " + string(data))
		return
	}
	pending, err := wager.NewBet(rand.Text(), request, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("BET_READY")
	if input.Kind == wager.RefundKind {
		refund, err := wager.NewRefund(pending.ID(), wager.RefundRequest{WinRequest: wager.WinRequest{BetRequest: pending.Request(), ReferenceExternalTransactionID: input.Reference}}, pending.CreatedAt())
		if err != nil {
			t.Fatal(err)
		}
		result, replay, err := NewWinStore(pool).ProcessRefund(ctx, refund)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(betProcessOutput{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance().MinorUnits(), Replay: replay})
		fmt.Println("BET_RESULT " + string(data))
		return
	}
	if input.Kind == wager.RollbackKind {
		rollback, err := wager.NewRollback(pending.ID(), wager.RollbackRequest{WinRequest: wager.WinRequest{BetRequest: pending.Request(), ReferenceExternalTransactionID: input.Reference}}, pending.CreatedAt())
		if err != nil {
			t.Fatal(err)
		}
		result, replay, err := NewWinStore(pool).ProcessRollback(ctx, rollback)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(betProcessOutput{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance().MinorUnits(), Replay: replay})
		fmt.Println("BET_RESULT " + string(data))
		return
	}
	if input.Kind == wager.WinKind {
		win, err := wager.NewWin(pending.ID(), wager.WinRequest{BetRequest: pending.Request()}, pending.CreatedAt())
		if err != nil {
			t.Fatal(err)
		}
		result, replay, err := NewWinStore(pool).ProcessWin(ctx, win)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(betProcessOutput{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance().MinorUnits(), Replay: replay})
		fmt.Println("BET_RESULT " + string(data))
		return
	}
	result, replay, err := NewBetStore(pool).ProcessBet(ctx, pending)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(betProcessOutput{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance().MinorUnits(), Replay: replay})
	fmt.Println("BET_RESULT " + string(data))
}

func runBetProcesses(t *testing.T, ctx context.Context, pool *pgxpool.Pool, inputs []betProcessInput, release func()) []betProcessOutput {
	t.Helper()
	database, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal("URL de teste inválida")
	}
	parameters := database.Query()
	parameters.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	database.RawQuery = parameters.Encode()
	type child struct {
		cmd    *exec.Cmd
		lines  <-chan string
		result betProcessOutput
	}
	children := make([]child, len(inputs))
	for i, input := range inputs {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBetIndependentProcessHelper$")
		command.Env = append(os.Environ(), "JUNGLE_BET_CHILD=1", "JUNGLE_BET_DATABASE_URL="+database.String())
		command.Stdin = strings.NewReader(string(data))
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		// O helper nunca imprime a URL, tokens ou o erro de configuração do driver.
		command.Stderr = command.Stdout
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
		lines := make(chan string, 16)
		go func() {
			defer close(lines)
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				lines <- scanner.Text()
			}
		}()
		children[i] = child{cmd: command, lines: lines}
	}
	for _, child := range children {
		select {
		case line := <-child.lines:
			if line != "BET_READY" {
				t.Fatalf("Filho não ficou pronto: %s", line)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if release != nil {
		release()
	}
	results := make([]betProcessOutput, len(inputs))
	for i, child := range children {
		found := false
		var diagnostics []string
		for line := range child.lines {
			diagnostics = append(diagnostics, line)
			if strings.HasPrefix(line, "BET_RESULT ") {
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "BET_RESULT ")), &results[i]); err != nil {
					t.Fatal(err)
				}
				found = true
			} else if strings.Contains(line, "FAIL") {
				t.Errorf("Processo filho falhou: %s", line)
			}
		}
		if err := child.cmd.Wait(); err != nil || !found {
			t.Fatalf("Processo não concluiu a aposta: %v; %s", err, strings.Join(diagnostics, "\n"))
		}
	}
	return results
}

func TestBetConcurrencyAcrossThreeIndependentProcessesIntegration(t *testing.T) {
	ctx, pool := betTestDatabase(t)
	w := repositoryTestWallet(t, "distributed-wallet", "distributed-player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	inputs := []betProcessInput{
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet-a", Key: "key-a"}, {WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet-b", Key: "key-b"}, {WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet-a", Key: "key-a"},
	}
	results := runBetProcesses(t, ctx, pool, inputs, func() {
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	})
	unique := map[string]betProcessOutput{}
	replays := 0
	for _, result := range results {
		unique[result.TransactionID] = result
		if result.Replay {
			replays++
		}
	}
	if len(unique) != 2 || replays != 1 {
		t.Fatalf("Esperava duas operações e um replay: %+v", results)
	}
	processed, rejected := 0, 0
	for _, result := range unique {
		switch result.Status {
		case wager.Processed:
			processed++
		case wager.Rejected:
			rejected++
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("Esperava um débito e uma rejeição: %+v", results)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 2000 || loaded.Version() != 2 {
		t.Fatal("Lost update ou saldo negativo", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 2, 5})
	// Todos os processos anteriores terminaram. Três processos novos reconstroem
	// os resultados persistidos sem memória compartilhada ou novo débito.
	repeated := runBetProcesses(t, ctx, pool, inputs, nil)
	for _, result := range repeated {
		original, ok := unique[result.TransactionID]
		if !ok || !result.Replay || result.Status != original.Status || result.Balance != original.Balance {
			t.Fatalf("Replay após reinício mudou resultado: %+v", result)
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 2, 5})
}

func TestWinAndBetAcrossThreeIndependentProcessesIntegration(t *testing.T) {
	ctx, pool := winTestDatabase(t)
	w := repositoryTestWallet(t, "distributed-win", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	inputs := []betProcessInput{
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "win", Key: "win-key", Kind: wager.WinKind, Amount: "25.00"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet", Key: "bet-key", Kind: wager.BetKind, Amount: "80.00"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "win", Key: "win-key", Kind: wager.WinKind, Amount: "25.00"},
	}
	results := runBetProcesses(t, ctx, pool, inputs, func() {
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	})
	unique := map[string]betProcessOutput{}
	replays := 0
	for _, result := range results {
		if result.Status != wager.Processed {
			t.Fatal("Operação concorrente rejeitada indevidamente")
		}
		unique[result.TransactionID] = result
		if result.Replay {
			replays++
		}
	}
	if len(unique) != 2 || replays != 1 {
		t.Fatal("Crédito duplicado ou transação perdida")
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 4500 || loaded.Version() != 3 {
		t.Fatal("Concorrência perdeu atualização de crédito/débito", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 3, 6})
	for _, result := range runBetProcesses(t, ctx, pool, inputs, nil) {
		original, ok := unique[result.TransactionID]
		if !ok || !result.Replay || result.Balance != original.Balance {
			t.Fatal("Replay em novos processos mudou resultado")
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 3, 6})
}

func TestLossAndBetAcrossThreeIndependentProcessesIntegration(t *testing.T) {
	ctx, pool := lossTestDatabase(t)
	w := repositoryTestWallet(t, "distributed-loss", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	inputs := []betProcessInput{
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "loss", Key: "loss-key", Kind: wager.LossKind, Amount: "0.00"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "bet", Key: "bet-key", Kind: wager.BetKind, Amount: "80.00"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "loss", Key: "loss-key", Kind: wager.LossKind, Amount: "0000.00"},
	}
	results := runBetProcesses(t, ctx, pool, inputs, func() {
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	})
	unique := map[string]betProcessOutput{}
	replays := 0
	for _, result := range results {
		if result.Status != wager.Processed {
			t.Fatal("Operação válida não processada")
		}
		unique[result.TransactionID] = result
		if result.Replay {
			replays++
		}
	}
	if len(unique) != 2 || replays != 1 {
		t.Fatal("LOSS criou transações duplicadas")
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 2000 || loaded.Version() != 2 {
		t.Fatal("LOSS alterou versão ou interferiu no BET", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 2, 5})
	for _, result := range runBetProcesses(t, ctx, pool, inputs, nil) {
		original, exists := unique[result.TransactionID]
		if !exists || !result.Replay || result.Balance != original.Balance {
			t.Fatal("Replay após término dos processos mudou o resultado")
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 2, 5})
}
