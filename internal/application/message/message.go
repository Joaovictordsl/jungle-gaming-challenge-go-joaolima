package message

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/contract/strictjson"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const MaxBodyBytes = 16384

var ErrInvalidEnvelope = errors.New("envelope inválido")
var ErrInboxConflict = errors.New("messageId reutilizado com outro conteúdo")

type Failure struct{ Code string }

func (f *Failure) Error() string { return "mensagem encerrada: " + f.Code }

type Envelope struct {
	MessageID  string
	OccurredAt time.Time
	Command    appwager.Command
}

func Parse(body []byte) (Envelope, error) {
	var e Envelope
	if len(body) > MaxBodyBytes {
		return e, ErrInvalidEnvelope
	}
	f, err := strictjson.Object(body, "messageId", "type", "occurredAt", "data")
	if err != nil {
		return e, ErrInvalidEnvelope
	}
	var id, kind, at *string
	if json.Unmarshal(f["messageId"], &id) != nil || id == nil || !query.ValidID(*id) || json.Unmarshal(f["type"], &kind) != nil || kind == nil || *kind != "WagerTransactionRequested" || json.Unmarshal(f["occurredAt"], &at) != nil || at == nil {
		return e, ErrInvalidEnvelope
	}
	e.MessageID = *id
	e.OccurredAt, err = time.Parse(time.RFC3339Nano, *at)
	if err != nil || e.OccurredAt.IsZero() {
		return Envelope{}, ErrInvalidEnvelope
	}
	e.OccurredAt = e.OccurredAt.UTC()
	required := []string{"providerId", "externalTransactionId", "idempotencyKey", "walletId", "playerId", "roundId", "gameId", "kind", "money"}
	data, err := strictjson.Fields(f["data"], required, []string{"referenceExternalTransactionId"})
	if err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	var key *string
	if json.Unmarshal(data["idempotencyKey"], &key) != nil || key == nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	delete(data, "idempotencyKey")
	commandBody, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	e.Command, _, err = appwager.DecodeCommand(commandBody, *key)
	if err != nil {
		return Envelope{}, errors.Join(ErrInvalidEnvelope, err)
	}
	return e, nil
}
func (e Envelope) Validate() error {
	if !query.ValidID(e.MessageID) || e.OccurredAt.IsZero() {
		return ErrInvalidEnvelope
	}
	_, err := e.Command.PayloadHash()
	return err
}
func (e Envelope) Hash() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{"messageId": e.MessageID, "type": "WagerTransactionRequested", "occurredAt": e.OccurredAt.UTC().Format(time.RFC3339Nano), "data": e.Command.Fields()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func GroupID(walletID string) string {
	sum := sha256.Sum256([]byte(walletID))
	return "wallet:" + hex.EncodeToString(sum[:])
}
func (e Envelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"messageId": e.MessageID, "type": "WagerTransactionRequested", "occurredAt": e.OccurredAt.UTC().Format(time.RFC3339Nano), "data": e.Command.Fields()})
}

type Handler func(context.Context, appwager.Stores) (appwager.Result, error)
type Inbox interface {
	Process(context.Context, string, Envelope, bool, Handler) (appwager.Result, bool, error)
}
type Service struct {
	inbox    Inbox
	dispatch *appwager.Dispatcher
}

func NewService(inbox Inbox, dispatch *appwager.Dispatcher) *Service {
	return &Service{inbox: inbox, dispatch: dispatch}
}
func (s *Service) Execute(ctx context.Context, consumer string, e Envelope, finalAttempt bool) (appwager.Result, bool, error) {
	if err := e.Validate(); err != nil {
		return appwager.Result{}, false, err
	}
	if strings.TrimSpace(consumer) == "" || len(consumer) > 256 {
		return appwager.Result{}, false, fmt.Errorf("consumidor inválido")
	}
	return s.inbox.Process(ctx, consumer, e, finalAttempt, func(ctx context.Context, stores appwager.Stores) (appwager.Result, error) {
		return s.dispatch.Execute(ctx, stores, e.Command)
	})
}
func PermanentCode(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	switch {
	case errors.Is(err, ErrInboxConflict):
		return "inbox_payload_conflict"
	case errors.Is(err, ErrInvalidEnvelope):
		return "invalid_envelope"
	case errors.Is(err, wager.ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, wager.ErrExternalConflict):
		return "external_transaction_conflict"
	case errors.Is(err, wallet.ErrNotFound):
		return "wallet_not_found"
	case errors.Is(err, wager.ErrWalletMismatch):
		return "wallet_mismatch"
	case errors.Is(err, wager.ErrInvalidBet), errors.Is(err, wager.ErrInvalidWin), errors.Is(err, wager.ErrInvalidLoss), errors.Is(err, wager.ErrInvalidRefund), errors.Is(err, wager.ErrInvalidRollback):
		return "invalid_operation"
	}
	return ""
}
