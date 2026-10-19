package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
)

const principalKey = "jungle.auth.principal"
const providerKey = "jungle.auth.provider"

// AuthorizedProvider consulta exclusivamente o vínculo decidido pelo middleware.
func AuthorizedProvider(c *gin.Context) (string, bool) {
	value, ok := c.Get(providerKey)
	provider, valid := value.(string)
	return provider, ok && valid && provider != ""
}

func (m *Authentication) RequireProviderPermission(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := m.authenticate(c)
		if !ok {
			return
		}
		provider, known := m.cfg.ProviderClients[principal.ClientID()]
		if !known || provider == "" || principal.ClientID() == m.cfg.InternalClientID || !principal.HasRole(permission) {
			c.Header("Cache-Control", "no-store")
			c.Header("WWW-Authenticate", "Bearer realm="+strconv.Quote(m.cfg.Audience)+`, error="insufficient_scope"`)
			c.AbortWithStatusJSON(nethttp.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Set(providerKey, provider)
		c.Next()
	}
}

type Authentication struct {
	verifier auth.TokenVerifier
	cfg      config.Auth
}

func NewAuthentication(verifier auth.TokenVerifier, cfg config.Auth) *Authentication {
	return &Authentication{verifier: verifier, cfg: cfg}
}

func AuthenticatedPrincipal(c *gin.Context) (auth.Principal, bool) {
	value, exists := c.Get(principalKey)
	principal, ok := value.(auth.Principal)
	return principal, exists && ok
}

// Authenticate disponibiliza somente claims provenientes de um token verificado.
func (m *Authentication) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := m.authenticate(c); ok {
			c.Next()
		}
	}
}

// RequireInternalPermission autentica e autoriza antes de executar o handler.
// A permissão isolada não permite que outro cliente atue como serviço interno.
func (m *Authentication) RequireInternalPermission(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := m.authenticate(c)
		if !ok {
			return
		}
		if principal.ClientID() != m.cfg.InternalClientID || !principal.HasRole(permission) {
			c.Header("WWW-Authenticate", "Bearer realm="+strconv.Quote(m.cfg.Audience)+`, error="insufficient_scope"`)
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(nethttp.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Next()
	}
}

func (m *Authentication) authenticate(c *gin.Context) (auth.Principal, bool) {
	values := c.Request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > 16<<10 {
		m.unauthorized(c, false)
		return auth.Principal{}, false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		m.unauthorized(c, false)
		return auth.Principal{}, false
	}
	principal, err := m.verifier.Verify(c.Request.Context(), parts[1])
	if err != nil {
		if errors.Is(err, auth.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(nethttp.StatusServiceUnavailable, gin.H{"error": "authentication_unavailable"})
		} else {
			m.unauthorized(c, true)
		}
		return auth.Principal{}, false
	}
	if strings.TrimSpace(principal.Subject()) == "" || strings.TrimSpace(principal.ClientID()) == "" {
		m.unauthorized(c, true)
		return auth.Principal{}, false
	}
	c.Set(principalKey, principal)
	return principal, true
}

func (m *Authentication) unauthorized(c *gin.Context, invalidToken bool) {
	challenge := "Bearer realm=" + strconv.Quote(m.cfg.Audience)
	if invalidToken {
		challenge += `, error="invalid_token"`
	}
	c.Header("WWW-Authenticate", challenge)
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(nethttp.StatusUnauthorized, gin.H{"error": "unauthorized"})
}

// RequireTransactionRead fixa o escopo antes da consulta; IDs internos de outro
// provedor não são carregados para depois decidir a autorização.
func (m *Authentication) RequireTransactionRead() gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := m.authenticate(c)
		if !ok {
			return
		}
		if principal.ClientID() == m.cfg.InternalClientID && principal.HasRole("wallets:read") {
			c.Next()
			return
		}
		provider, known := m.cfg.ProviderClients[principal.ClientID()]
		if principal.ClientID() != m.cfg.InternalClientID && known && provider != "" && principal.HasRole("wagering:read") {
			c.Set(providerKey, provider)
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("WWW-Authenticate", "Bearer realm="+strconv.Quote(m.cfg.Audience)+`, error="insufficient_scope"`)
		c.AbortWithStatusJSON(nethttp.StatusForbidden, gin.H{"error": "forbidden"})
	}
}
