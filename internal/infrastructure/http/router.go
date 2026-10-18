package http

import (
	"io"
	"log/slog"
	nethttp "net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func NewRouter(logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.ForwardedByClientIP = false
	_ = router.SetTrustedProxies(nil)
	router.HandleMethodNotAllowed = true
	router.RedirectTrailingSlash = false
	router.Use(requestLogger(logger))
	// O recovery padrão escreve headers e stack trace. Aqui o erro usa o logger
	// JSON e não inclui tokens, payloads ou o valor recuperado do panic.
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		logger.Error("Falha inesperada na requisição HTTP", "method", c.Request.Method, "route", c.FullPath())
		c.AbortWithStatusJSON(nethttp.StatusInternalServerError, gin.H{"error": "internal_error"})
	}))
	router.NoRoute(func(c *gin.Context) {
		c.JSON(nethttp.StatusNotFound, gin.H{"error": "not_found"})
	})
	router.NoMethod(func(c *gin.Context) {
		c.JSON(nethttp.StatusMethodNotAllowed, gin.H{"error": "method_not_allowed"})
	})
	router.GET("/health/live", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(nethttp.StatusOK, gin.H{"status": "ok"})
	})
	return router
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		logger.Info("Requisição HTTP", "method", c.Request.Method, "route", route,
			"status", c.Writer.Status(), "duration", time.Since(startedAt))
	}
}
