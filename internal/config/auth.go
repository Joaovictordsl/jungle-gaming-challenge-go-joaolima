package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Auth struct {
	IssuerURL        string
	Audience         string
	InternalClientID string
	HTTPTimeout      time.Duration
}

func LoadAuth() (Auth, error) {
	cfg := Auth{IssuerURL: "http://localhost:8081/realms/jungle", Audience: "jungle-api",
		InternalClientID: "jungle-internal", HTTPTimeout: 3 * time.Second}
	for key, target := range map[string]*string{
		"OIDC_ISSUER_URL": &cfg.IssuerURL, "OIDC_AUDIENCE": &cfg.Audience, "OIDC_INTERNAL_CLIENT_ID": &cfg.InternalClientID,
	} {
		if value, exists := os.LookupEnv(key); exists {
			*target = value
		}
	}
	if value, exists := os.LookupEnv("OIDC_HTTP_TIMEOUT"); exists {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Auth{}, fmt.Errorf("%w: OIDC_HTTP_TIMEOUT deve ser uma duração positiva", ErrInvalidConfig)
		}
		cfg.HTTPTimeout = timeout
	}
	if err := cfg.Validate(); err != nil {
		return Auth{}, err
	}
	return cfg, nil
}

func (c Auth) Validate() error {
	issuer, err := url.Parse(c.IssuerURL)
	if err != nil || issuer.Hostname() == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || strings.HasSuffix(c.IssuerURL, "/") {
		return fmt.Errorf("%w: OIDC_ISSUER_URL deve ser uma URL de issuer sem credenciais, query, fragmento ou barra final", ErrInvalidConfig)
	}
	local := issuer.Hostname() == "localhost"
	if ip := net.ParseIP(issuer.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if issuer.Scheme != "https" && !(issuer.Scheme == "http" && local) {
		return fmt.Errorf("%w: OIDC_ISSUER_URL exige HTTPS, exceto em loopback local", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.Audience) == "" || strings.ContainsAny(c.Audience, " \t\r\n\"") {
		return fmt.Errorf("%w: OIDC_AUDIENCE deve ser preenchida e não conter espaços ou aspas", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.InternalClientID) == "" || strings.TrimSpace(c.InternalClientID) != c.InternalClientID {
		return fmt.Errorf("%w: OIDC_INTERNAL_CLIENT_ID deve ser preenchido", ErrInvalidConfig)
	}
	if c.HTTPTimeout <= 0 {
		return fmt.Errorf("%w: OIDC_HTTP_TIMEOUT deve ser positivo", ErrInvalidConfig)
	}
	return nil
}
