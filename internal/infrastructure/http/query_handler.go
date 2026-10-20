package http

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
)

type QueryHandler struct {
	queries   *query.Service
	metrics   *metrics.Reconciliation
	messaging *metrics.Messaging
	logger    *slog.Logger
	collector *metrics.Collector
}

func NewQueryHandler(queries *query.Service, metrics *metrics.Reconciliation, messaging *metrics.Messaging, collector *metrics.Collector, logger *slog.Logger) *QueryHandler {
	return &QueryHandler{queries: queries, metrics: metrics, messaging: messaging, collector: collector, logger: logger}
}
func RegisterQueryRoutes(router *gin.Engine, auth *Authentication, h *QueryHandler) {
	router.GET("/wagering/transactions/:transactionId", auth.RequireTransactionRead(), h.FindTransaction)
	router.GET("/providers/:providerId/wagering/transactions/:externalTransactionId", auth.RequireTransactionRead(), h.FindExternal)
	router.GET("/wallets/:walletId/ledger", auth.RequireInternalPermission("wallets:read"), h.Ledger)
	router.POST("/wallets/:walletId/reconciliation", auth.RequireInternalPermission("wallets:read"), h.Reconcile)
	router.GET("/metrics", auth.RequireInternalPermission("wallets:read"), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(200, "text/plain; version=0.0.4; charset=utf-8", []byte(h.metrics.Prometheus()+h.messaging.Prometheus()+h.collector.Prometheus(c.Request.Context())))
	})
}
func (h *QueryHandler) FindTransaction(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	scope, _ := AuthorizedProvider(c)
	result, err := h.queries.FindTransaction(c.Request.Context(), c.Param("transactionId"), scope)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(200, result)
}
func (h *QueryHandler) FindExternal(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	scope, _ := AuthorizedProvider(c)
	result, err := h.queries.FindExternal(c.Request.Context(), c.Param("providerId"), c.Param("externalTransactionId"), scope)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(200, result)
}
func (h *QueryHandler) Ledger(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	params, err := strictQuery(c, "cursor", "limit")
	if err != nil {
		h.fail(c, err)
		return
	}
	limit := 50
	if raw, ok := params["limit"]; ok {
		if raw == "" {
			h.fail(c, query.ErrInvalidQuery)
			return
		}
		for _, digit := range raw {
			if digit < '0' || digit > '9' {
				h.fail(c, query.ErrInvalidQuery)
				return
			}
		}
		limit, err = strconv.Atoi(raw)
		if err != nil {
			h.fail(c, query.ErrInvalidQuery)
			return
		}
	}
	if cursor, ok := params["cursor"]; ok && cursor == "" {
		h.fail(c, query.ErrInvalidCursor)
		return
	}
	result, err := h.queries.ListLedger(c.Request.Context(), c.Param("walletId"), params["cursor"], limit)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(200, result)
}
func (h *QueryHandler) Reconcile(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) != 0 {
		h.fail(c, query.ErrInvalidQuery)
		return
	}
	if _, err := strictQuery(c); err != nil {
		h.fail(c, err)
		return
	}
	result, err := h.queries.Reconcile(c.Request.Context(), c.Param("walletId"))
	if err != nil {
		h.fail(c, err)
		return
	}
	if !result.Consistent {
		h.logger.Warn("Divergência na reconciliação", "walletId", result.WalletID, "checkedEntries", result.CheckedEntries)
	}
	c.JSON(200, result)
}
func strictQuery(c *gin.Context, allowed ...string) (map[string]string, error) {
	// ParseQuery rejeita escapes e separadores inválidos em vez de ignorá-los.
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, query.ErrInvalidQuery
	}
	result := make(map[string]string, len(values))
	for name, list := range values {
		known := false
		for _, key := range allowed {
			if key == name {
				known = true
			}
		}
		if !known || len(list) != 1 {
			return nil, query.ErrInvalidQuery
		}
		result[name] = list[0]
	}
	return result, nil
}
func (h *QueryHandler) fail(c *gin.Context, err error) {
	status, code := 500, "internal_error"
	switch {
	case errors.Is(err, query.ErrInvalidCursor):
		status, code = 400, "invalid_cursor"
	case errors.Is(err, query.ErrInvalidQuery):
		status, code = 400, "invalid_request"
	case errors.Is(err, query.ErrNotFound):
		status, code = 404, "transaction_not_found"
	case errors.Is(err, wallet.ErrNotFound):
		status, code = 404, "wallet_not_found"
	case errors.Is(err, query.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, wallet.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = 503, "service_unavailable"
	}
	if status >= 500 {
		h.logger.Error("Falha na consulta financeira", "error_code", code)
	}
	c.JSON(status, gin.H{"error": code})
}
