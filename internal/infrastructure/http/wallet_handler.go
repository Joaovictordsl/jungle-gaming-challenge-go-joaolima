package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	nethttp "net/http"
	"net/url"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const maxWalletBodyBytes = 16 << 10

var errInvalidWalletJSON = errors.New("JSON de abertura inválido")

type WalletHandler struct {
	open       *applicationwallet.OpenWallet
	repository wallet.Repository
	logger     *slog.Logger
}

func NewWalletHandler(open *applicationwallet.OpenWallet, repository wallet.Repository, logger *slog.Logger) *WalletHandler {
	return &WalletHandler{open: open, repository: repository, logger: logger}
}

func RegisterWalletRoutes(router *gin.Engine, authentication *Authentication, handler *WalletHandler) {
	router.POST("/wallets", authentication.RequireInternalPermission("wallets:write"), handler.Open)
	router.GET("/wallets/:walletId", authentication.RequireInternalPermission("wallets:read"), handler.FindByID)
}

type walletResponse struct {
	ID       string      `json:"id"`
	PlayerID string      `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

func (h *WalletHandler) Open(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		c.JSON(nethttp.StatusUnsupportedMediaType, gin.H{"error": "unsupported_media_type"})
		return
	}
	c.Request.Body = nethttp.MaxBytesReader(c.Writer, c.Request.Body, maxWalletBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *nethttp.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(nethttp.StatusRequestEntityTooLarge, gin.H{"error": "body_too_large"})
		} else if c.Request.Context().Err() != nil {
			h.fail(c, "open", c.Request.Context().Err())
		} else {
			c.JSON(nethttp.StatusBadRequest, gin.H{"error": "invalid_request"})
		}
		return
	}
	playerID, amount, currency, err := decodeWalletRequest(body)
	if err != nil {
		c.JSON(nethttp.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	balance, err := money.New(amount, currency)
	if err != nil {
		h.fail(c, "open", err)
		return
	}
	w, err := h.open.Execute(c.Request.Context(), applicationwallet.OpenWalletInput{PlayerID: playerID, InitialBalance: balance})
	if err != nil {
		h.fail(c, "open", err)
		return
	}
	if err := w.Validate(); err != nil {
		h.fail(c, "open_result", errors.New("estado retornado inválido"))
		return
	}
	c.Header("Location", "/wallets/"+url.PathEscape(w.ID()))
	h.logger.Info("Carteira aberta", "walletId", w.ID(), "walletVersion", w.Version())
	c.JSON(nethttp.StatusCreated, walletResponse{ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version()})
}

func (h *WalletHandler) FindByID(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	w, err := h.repository.FindByID(c.Request.Context(), c.Param("walletId"))
	if err != nil {
		h.fail(c, "find", err)
		return
	}
	if err := w.Validate(); err != nil {
		h.fail(c, "find_result", errors.New("estado retornado inválido"))
		return
	}
	c.JSON(nethttp.StatusOK, walletResponse{ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version()})
}

func (h *WalletHandler) fail(c *gin.Context, operation string, err error) {
	status, code := nethttp.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, wallet.ErrNotFound):
		status, code = nethttp.StatusNotFound, "wallet_not_found"
	case errors.Is(err, wallet.ErrAlreadyExists):
		status, code = nethttp.StatusConflict, "wallet_already_exists"
	case errors.Is(err, wallet.ErrInvalidID):
		status, code = nethttp.StatusBadRequest, "invalid_wallet_id"
	case errors.Is(err, wallet.ErrInvalidPlayerID):
		status, code = nethttp.StatusBadRequest, "invalid_player_id"
	case errors.Is(err, money.ErrInvalidCurrency):
		status, code = nethttp.StatusBadRequest, "invalid_currency"
	case errors.Is(err, money.ErrInvalidAmount), errors.Is(err, money.ErrOverflow), errors.Is(err, wallet.ErrNegativeBalance):
		status, code = nethttp.StatusBadRequest, "invalid_amount"
	case errors.Is(err, wallet.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = nethttp.StatusServiceUnavailable, "service_unavailable"
	}
	if status >= 500 {
		// Causas do driver podem conter detalhes de conexão ou valores do SQL.
		h.logger.Error("Falha na operação de carteira", "operation", operation, "error_code", code)
	}
	c.JSON(status, gin.H{"error": code})
}

// A leitura por objetos preserva valores como RawMessage: dinheiro nunca passa
// por float e chaves duplicadas não são silenciosamente sobrescritas.
func decodeWalletRequest(body []byte) (playerID, amount, currency string, err error) {
	if !utf8.Valid(body) {
		return "", "", "", errInvalidWalletJSON
	}
	fields, err := walletJSONObject(body, "playerId", "initialBalance")
	if err != nil {
		return "", "", "", err
	}
	initial, err := walletJSONObject(fields["initialBalance"], "amount", "currency")
	if err != nil {
		return "", "", "", err
	}
	for key, target := range map[string]*string{"playerId": &playerID, "amount": &amount, "currency": &currency} {
		source := initial
		if key == "playerId" {
			source = fields
		}
		var value *string
		if json.Unmarshal(source[key], &value) != nil || value == nil {
			return "", "", "", errInvalidWalletJSON
		}
		*target = *value
	}
	return playerID, amount, currency, nil
}

func walletJSONObject(body []byte, allowed ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errInvalidWalletJSON
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errInvalidWalletJSON
		}
		key, ok := token.(string)
		if !ok {
			return nil, errInvalidWalletJSON
		}
		known := false
		for _, candidate := range allowed {
			if key == candidate {
				known = true
				break
			}
		}
		if _, duplicate := fields[key]; !known || duplicate {
			return nil, errInvalidWalletJSON
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errInvalidWalletJSON
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(fields) != len(allowed) {
		return nil, errInvalidWalletJSON
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errInvalidWalletJSON
	}
	return fields, nil
}
