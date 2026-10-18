package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/config"
)

var (
	ErrInvalidToken = errors.New("token de acesso inválido")
	ErrUnavailable  = errors.New("autenticação temporariamente indisponível")
)

var Module = fx.Module("auth",
	fx.Provide(config.LoadAuth, NewVerifier),
	fx.Provide(func(v *Verifier) TokenVerifier { return v }),
)

type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (Principal, error)
}

// Principal contém somente identidade e papéis verificados, sem o token bruto.
type Principal struct {
	subject, clientID string
	roles             map[string]struct{}
}

func (p Principal) Subject() string  { return p.subject }
func (p Principal) ClientID() string { return p.clientID }
func (p Principal) HasRole(role string) bool {
	_, exists := p.roles[role]
	return role != "" && exists
}

type Verifier struct {
	cfg      config.Auth
	mu       sync.RWMutex
	verifier *oidc.IDTokenVerifier
}

func NewVerifier(lifecycle fx.Lifecycle, cfg config.Auth, logger *slog.Logger) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Timeout: cfg.HTTPTimeout, Transport: authTransport{base: transport}}
	keyContext, cancelKeys := context.WithCancel(context.Background())
	keyContext = oidc.ClientContext(keyContext, client)
	v := &Verifier{cfg: cfg}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			startupCtx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
			defer cancel()
			provider, err := oidc.NewProvider(oidc.ClientContext(startupCtx, client), cfg.IssuerURL)
			if err != nil {
				cancelKeys()
				transport.CloseIdleConnections()
				if err := ctx.Err(); err != nil {
					return fmt.Errorf("%w: %w", ErrUnavailable, err)
				}
				return ErrUnavailable
			}
			var metadata struct {
				JWKSURL string `json:"jwks_uri"`
			}
			if provider.Claims(&metadata) != nil || !validJWKSURL(metadata.JWKSURL, cfg.IssuerURL) {
				cancelKeys()
				transport.CloseIdleConnections()
				return ErrUnavailable
			}
			keys := classifiedKeySet{remote: oidc.NewRemoteKeySet(keyContext, metadata.JWKSURL)}
			verifier := oidc.NewVerifier(cfg.IssuerURL, keys, &oidc.Config{
				ClientID: cfg.Audience, SupportedSigningAlgs: []string{oidc.RS256},
			})
			v.mu.Lock()
			v.verifier = verifier
			v.mu.Unlock()
			logger.Info("Provedor OIDC inicializado")
			return nil
		},
		OnStop: func(context.Context) error {
			v.mu.Lock()
			v.verifier = nil
			v.mu.Unlock()
			cancelKeys()
			transport.CloseIdleConnections()
			return nil
		},
	})
	return v, nil
}

// validJWKSURL restringe a obtenção de chaves à origem do IdP configurado.
func validJWKSURL(raw, issuerURL string) bool {
	keyURL, err := url.Parse(raw)
	if err != nil || keyURL.User != nil || keyURL.RawQuery != "" || keyURL.Fragment != "" {
		return false
	}
	issuer, err := url.Parse(issuerURL)
	return err == nil && keyURL.Scheme == issuer.Scheme && keyURL.Host == issuer.Host && keyURL.Path != ""
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if rawToken == "" || len(rawToken) > 16<<10 {
		return Principal{}, ErrInvalidToken
	}
	v.mu.RLock()
	verifier := v.verifier
	v.mu.RUnlock()
	if verifier == nil {
		return Principal{}, ErrUnavailable
	}
	verificationCtx, cancel := context.WithTimeout(ctx, v.cfg.HTTPTimeout)
	defer cancel()
	state := &verificationState{}
	verificationCtx = context.WithValue(verificationCtx, verificationStateKey{}, state)
	token, err := verifier.Verify(verificationCtx, rawToken)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return Principal{}, err
		}
		if state.unavailable || verificationCtx.Err() != nil {
			return Principal{}, ErrUnavailable
		}
		return Principal{}, ErrInvalidToken
	}
	var claims struct {
		Type           string `json:"typ"`
		ClientID       string `json:"azp"`
		NotBefore      *int64 `json:"nbf"`
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	if token.Claims(&claims) != nil {
		return Principal{}, ErrInvalidToken
	}
	now := time.Now()
	if claims.Type != "Bearer" || strings.TrimSpace(token.Subject) == "" || strings.TrimSpace(claims.ClientID) == "" ||
		token.IssuedAt.IsZero() || token.IssuedAt.After(now.Add(30*time.Second)) || !token.Expiry.After(token.IssuedAt) ||
		(claims.NotBefore != nil && *claims.NotBefore > now.Add(30*time.Second).Unix()) {
		return Principal{}, ErrInvalidToken
	}
	roles := make(map[string]struct{})
	for _, role := range claims.ResourceAccess[v.cfg.Audience].Roles {
		if role != "" {
			roles[role] = struct{}{}
		}
	}
	return Principal{subject: token.Subject, clientID: claims.ClientID, roles: roles}, nil
}

// go-oidc formata alguns erros sem preservar errors.Is. A classificação é
// capturada na fronteira KeySet, antes dessa formatação, para distinguir 503 de 401.
type verificationStateKey struct{}
type verificationState struct{ unavailable bool }
type classifiedKeySet struct{ remote oidc.KeySet }

func (k classifiedKeySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	payload, err := k.remote.VerifySignature(ctx, raw)
	if err != nil {
		var networkError *url.Error
		if errors.Is(err, ErrUnavailable) || errors.As(err, &networkError) || ctx.Err() != nil {
			if state, ok := ctx.Value(verificationStateKey{}).(*verificationState); ok {
				state.unavailable = true
			}
		}
	}
	return payload, err
}

type authTransport struct{ base *http.Transport }

func (t authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, ErrUnavailable
	}
	return response, nil
}
