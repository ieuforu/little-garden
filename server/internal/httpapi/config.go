package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Development                           bool
	Listen, Origin, Database, AdminSocket string
	Secret                                []byte
	TurnstileSiteKey, TurnstileSecret     string
	TrustedProxies                        []netip.Prefix
	MaxConnections                        int
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func LoadConfig() (Config, error) {
	c := Config{Development: os.Getenv("GARDEN_MODE") == "development", Listen: env("LISTEN_ADDR", ":8080"), Origin: strings.TrimSuffix(os.Getenv("PUBLIC_ORIGIN"), "/"), Database: env("DATABASE_PATH", "/data/garden.db"), AdminSocket: env("ADMIN_SOCKET", "/data/admin.sock"), TurnstileSiteKey: os.Getenv("TURNSTILE_SITE_KEY"), TurnstileSecret: os.Getenv("TURNSTILE_SECRET_KEY"), MaxConnections: 200}
	if mode := os.Getenv("GARDEN_MODE"); mode != "" && mode != "production" && mode != "development" {
		return c, errors.New("GARDEN_MODE must be production or development")
	}
	if value := os.Getenv("MAX_WEBSOCKETS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 2000 {
			return c, errors.New("MAX_WEBSOCKETS must be between 1 and 2000")
		}
		c.MaxConnections = n
	}
	if value := os.Getenv("SESSION_SECRET"); value != "" {
		secret, err := hex.DecodeString(value)
		if err != nil || len(secret) != 32 {
			return c, errors.New("SESSION_SECRET must contain exactly 64 hexadecimal characters")
		}
		c.Secret = secret
	}
	if c.Development {
		host, _, err := net.SplitHostPort(c.Listen)
		if err != nil {
			return c, err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return c, errors.New("development mode must listen on a loopback IP")
		}
		if len(c.Secret) == 0 {
			c.Secret = make([]byte, 32)
			if _, err := rand.Read(c.Secret); err != nil {
				return c, err
			}
		}
	} else {
		u, err := url.Parse(c.Origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("PUBLIC_ORIGIN must be a single HTTPS origin")
		}
		if len(c.Secret) != 32 || c.TurnstileSiteKey == "" || c.TurnstileSecret == "" {
			return c, errors.New("production requires SESSION_SECRET, TURNSTILE_SITE_KEY and TURNSTILE_SECRET_KEY")
		}
		if strings.HasPrefix(c.TurnstileSiteKey, "1x000000") || strings.HasPrefix(c.TurnstileSiteKey, "2x000000") || strings.HasPrefix(c.TurnstileSiteKey, "3x000000") {
			return c, errors.New("Turnstile testing keys are not allowed in production")
		}
	}
	for _, value := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if strings.TrimSpace(value) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Bits() == 0 {
			return c, errors.New("TRUSTED_PROXIES must list explicit proxy CIDRs, never a default route")
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix)
	}
	return c, nil
}
