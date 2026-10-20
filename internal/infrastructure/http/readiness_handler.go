package http

import (
	"github.com/gin-gonic/gin"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/health"
	"net/http"
)

type ReadinessHandler struct{ checker health.Checker }

func NewReadinessHandler(checker health.Checker) *ReadinessHandler {
	return &ReadinessHandler{checker: checker}
}
func RegisterReadinessRoutes(router *gin.Engine, h *ReadinessHandler) {
	router.GET("/health/ready", h.Check)
}
func (h *ReadinessHandler) Check(c *gin.Context) {
	checks := h.checker.Check(c.Request.Context())
	code, status := http.StatusServiceUnavailable, "not_ready"
	if checks.Ready() {
		code, status = http.StatusOK, "ready"
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(code, struct {
		Status string        `json:"status"`
		Checks health.Checks `json:"checks"`
	}{status, checks})
}
