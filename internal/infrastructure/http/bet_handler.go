package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	nethttp "net/http"

	"github.com/gin-gonic/gin"

	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

type BetHandler struct {
	process  *applicationwager.ProcessBet
	win      *applicationwager.ProcessWin
	loss     *applicationwager.ProcessLoss
	refund   *applicationwager.ProcessRefund
	rollback *applicationwager.ProcessRollback
	logger   *slog.Logger
}

func NewBetHandler(process *applicationwager.ProcessBet, win *applicationwager.ProcessWin, loss *applicationwager.ProcessLoss, refund *applicationwager.ProcessRefund, rollback *applicationwager.ProcessRollback, logger *slog.Logger) *BetHandler {
	return &BetHandler{process: process, win: win, loss: loss, refund: refund, rollback: rollback, logger: logger}
}
func RegisterBetRoutes(router *gin.Engine, authentication *Authentication, handler *BetHandler) {
	router.POST("/wagering/transactions", authentication.RequireProviderPermission("wagering:write"), handler.Submit)
}

type betResponse struct {
	TransactionID    string      `json:"transactionId"`
	Status           string      `json:"status"`
	Balance          money.Money `json:"balance"`
	FailureCode      string      `json:"failureCode,omitempty"`
	IdempotentReplay bool        `json:"idempotentReplay"`
}

func (h *BetHandler) Submit(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	keys := c.Request.Header.Values("Idempotency-Key")
	if len(keys) != 1 || keys[0] == "" || len(keys[0]) > 256 {
		betError(c, 400, "invalid_idempotency_key")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		betError(c, 415, "unsupported_media_type")
		return
	}
	c.Request.Body = nethttp.MaxBytesReader(c.Writer, c.Request.Body, maxWalletBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var sizeError *nethttp.MaxBytesError
		if errors.As(err, &sizeError) {
			betError(c, 413, "body_too_large")
		} else if c.Request.Context().Err() != nil {
			betError(c, 503, "service_unavailable")
		} else {
			betError(c, 400, "invalid_request")
		}
		return
	}
	input, kind, err := decodeWagerRequest(body, keys[0])
	if err != nil {
		betError(c, 400, "invalid_request")
		return
	}
	provider, ok := AuthorizedProvider(c)
	if !ok || provider != input.ProviderID {
		betError(c, 403, "forbidden")
		return
	}
	var response betResponse
	var resultID, resultStatus string
	var replay bool
	if kind == wager.WinKind {
		result, wasReplay, processErr := h.win.Execute(c.Request.Context(), wager.WinRequest{BetRequest: input.BetRequest, ReferenceExternalTransactionID: input.ReferenceExternalTransactionID})
		err, replay = processErr, wasReplay
		if err == nil {
			if validateErr := result.Validate(); validateErr != nil {
				betError(c, 500, "internal_error")
				return
			}
			response = betResponse{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance(), FailureCode: result.FailureCode(), IdempotentReplay: replay}
			resultID, resultStatus = result.ID(), result.Status()
		}
	} else if kind == wager.RefundKind {
		result, wasReplay, processErr := h.refund.Execute(c.Request.Context(), wager.RefundRequest{WinRequest: wager.WinRequest{BetRequest: input.BetRequest, ReferenceExternalTransactionID: input.ReferenceExternalTransactionID}})
		err, replay = processErr, wasReplay
		if err == nil {
			if validateErr := result.Validate(); validateErr != nil {
				betError(c, 500, "internal_error")
				return
			}
			response = betResponse{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance(), FailureCode: result.FailureCode(), IdempotentReplay: replay}
			resultID, resultStatus = result.ID(), result.Status()
		}
	} else if kind == wager.RollbackKind {
		result, wasReplay, processErr := h.rollback.Execute(c.Request.Context(), wager.RollbackRequest{WinRequest: wager.WinRequest{BetRequest: input.BetRequest, ReferenceExternalTransactionID: input.ReferenceExternalTransactionID}})
		err, replay = processErr, wasReplay
		if err == nil {
			if validateErr := result.Validate(); validateErr != nil {
				betError(c, 500, "internal_error")
				return
			}
			response = betResponse{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance(), FailureCode: result.FailureCode(), IdempotentReplay: replay}
			resultID, resultStatus = result.ID(), result.Status()
		}
	} else if kind == wager.LossKind {
		result, wasReplay, processErr := h.loss.Execute(c.Request.Context(), wager.LossRequest{BetRequest: input.BetRequest})
		err, replay = processErr, wasReplay
		if err == nil {
			if validateErr := result.Validate(); validateErr != nil {
				betError(c, 500, "internal_error")
				return
			}
			response = betResponse{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance(), IdempotentReplay: replay}
			resultID, resultStatus = result.ID(), result.Status()
		}
	} else {
		result, wasReplay, processErr := h.process.Execute(c.Request.Context(), input.BetRequest)
		err, replay = processErr, wasReplay
		if err == nil {
			if validateErr := result.Validate(); validateErr != nil {
				betError(c, 500, "internal_error")
				return
			}
			response = betResponse{TransactionID: result.ID(), Status: result.Status(), Balance: result.Balance(), FailureCode: result.FailureCode(), IdempotentReplay: replay}
			resultID, resultStatus = result.ID(), result.Status()
		}
	}
	if err != nil {
		status, code := 500, "internal_error"
		switch {
		case errors.Is(err, wager.ErrInvalidRollback):
			status, code = 400, "invalid_rollback"
		case errors.Is(err, wager.ErrInvalidRefund):
			status, code = 400, "invalid_refund"
		case errors.Is(err, wager.ErrInvalidLoss):
			status, code = 400, "invalid_loss"
		case errors.Is(err, wager.ErrInvalidBet), errors.Is(err, wager.ErrInvalidWin), errors.Is(err, wager.ErrWalletMismatch):
			status, code = 400, "invalid_bet"
			if kind == wager.LossKind {
				code = "invalid_loss"
			}
			if kind == wager.RefundKind {
				code = "invalid_refund"
			}
			if kind == wager.RollbackKind {
				code = "invalid_rollback"
			}
		case errors.Is(err, wager.ErrIdempotencyConflict):
			status, code = 409, "idempotency_conflict"
		case errors.Is(err, wager.ErrExternalConflict):
			status, code = 409, "external_transaction_conflict"
		case errors.Is(err, wallet.ErrNotFound):
			status, code = 404, "wallet_not_found"
		case errors.Is(err, wallet.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = 503, "service_unavailable"
		}
		if status >= 500 {
			h.logger.Error("Falha no processamento financeiro", "kind", kind, "providerId", provider, "walletId", input.WalletID, "error_code", code)
		}
		betError(c, status, code)
		return
	}
	if resultStatus != wager.Processed && resultStatus != wager.Rejected && resultStatus != wager.PendingReference {
		betError(c, 500, "internal_error")
		return
	}
	status := nethttp.StatusOK
	if resultStatus == wager.Rejected {
		status = nethttp.StatusUnprocessableEntity
	} else if resultStatus == wager.PendingReference {
		status = nethttp.StatusAccepted
	}
	h.logger.Info("Operação financeira registrada", "kind", kind, "correlationId", resultID, "transactionId", resultID, "walletId", input.WalletID,
		"providerId", provider, "status", resultStatus, "idempotentReplay", replay)
	c.JSON(status, response)
}

func betError(c *gin.Context, status int, code string) { c.JSON(status, gin.H{"error": code}) }

type wagerRequest struct {
	wager.BetRequest
	ReferenceExternalTransactionID string
}

func decodeWagerRequest(body []byte, key string) (wagerRequest, string, error) {
	fields, err := walletJSONFields(body, []string{"providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind", "money"}, []string{"referenceExternalTransactionId"})
	if err != nil {
		return wagerRequest{}, "", err
	}
	values := make(map[string]string, 7)
	for _, name := range []string{"providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind"} {
		var value *string
		if json.Unmarshal(fields[name], &value) != nil || value == nil {
			return wagerRequest{}, "", errInvalidWalletJSON
		}
		values[name] = *value
	}
	if values["kind"] != wager.BetKind && values["kind"] != wager.WinKind && values["kind"] != wager.LossKind && values["kind"] != wager.RefundKind && values["kind"] != wager.RollbackKind {
		return wagerRequest{}, "", wager.ErrInvalidBet
	}
	var reference string
	if raw, exists := fields["referenceExternalTransactionId"]; exists {
		var value *string
		if (values["kind"] != wager.WinKind && values["kind"] != wager.RefundKind && values["kind"] != wager.RollbackKind) || json.Unmarshal(raw, &value) != nil || value == nil || *value == "" {
			return wagerRequest{}, "", wager.ErrInvalidWin
		}
		reference = *value
	}
	financial, err := walletJSONObject(fields["money"], "amount", "currency")
	if err != nil {
		return wagerRequest{}, "", err
	}
	var amount, currency *string
	if json.Unmarshal(financial["amount"], &amount) != nil || json.Unmarshal(financial["currency"], &currency) != nil || amount == nil || currency == nil {
		return wagerRequest{}, "", errInvalidWalletJSON
	}
	value, err := money.New(*amount, *currency)
	if err != nil {
		return wagerRequest{}, "", err
	}
	request := wager.BetRequest{ProviderID: values["providerId"], ExternalTransactionID: values["externalTransactionId"], IdempotencyKey: key,
		PlayerID: values["playerId"], WalletID: values["walletId"], RoundID: values["roundId"], GameID: values["gameId"], Money: value}
	decoded := wagerRequest{BetRequest: request, ReferenceExternalTransactionID: reference}
	switch values["kind"] {
	case wager.RollbackKind:
		err = (wager.RollbackRequest{WinRequest: wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: reference}}).Validate()
	case wager.RefundKind:
		err = (wager.RefundRequest{WinRequest: wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: reference}}).Validate()
	case wager.LossKind:
		err = (wager.LossRequest{BetRequest: request}).Validate()
	case wager.WinKind:
		err = (wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: reference}).Validate()
	default:
		err = request.Validate()
	}
	return decoded, values["kind"], err
}
