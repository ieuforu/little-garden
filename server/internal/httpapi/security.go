package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const sessionLife = 24 * time.Hour

type session struct {
	ID      string `json:"id"`
	Expires int64  `json:"expires"`
}

func (s *Server) cookieName() string {
	if s.cfg.Development {
		return "garden_session"
	}
	return "__Host-garden_session"
}
func (s *Server) sign(value string) string {
	h := hmac.New(sha256.New, s.cfg.Secret)
	h.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Server) session(r *http.Request) (session, bool) {
	var claims session
	cookie, err := r.Cookie(s.cookieName())
	if err != nil || len(cookie.Value) > 512 {
		return claims, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(s.sign(parts[0])), []byte(parts[1])) {
		return claims, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(data, &claims) != nil || len(claims.ID) != 32 || claims.Expires <= time.Now().Unix() {
		return session{}, false
	}
	if s.garden.Banned(claims.ID) {
		return session{}, false
	}
	return claims, true
}
func (s *Server) issueSession(w http.ResponseWriter) (session, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return session{}, err
	}
	claims := session{ID: hex.EncodeToString(random[:]), Expires: time.Now().Add(sessionLife).Unix()}
	data, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(data)
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: body + "." + s.sign(body), Path: "/", MaxAge: int(sessionLife.Seconds()), Expires: time.Unix(claims.Expires, 0), HttpOnly: true, Secure: !s.cfg.Development, SameSite: http.SameSiteStrictMode})
	return claims, nil
}

func (s *Server) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if !s.cfg.Development {
		return origin == s.cfg.Origin
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	for _, prefix := range s.cfg.TrustedProxies {
		if prefix.Contains(peer) {
			// Only this header, overwritten by the trusted Caddy listener, is honored.
			if ip, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil {
				peer = ip.Unmap()
			}
			break
		}
	}
	// Group IPv6 privacy addresses by /64 to avoid trivial address rotation.
	if peer.Is6() {
		return netip.PrefixFrom(peer, 64).Masked().String()
	}
	return peer.String()
}

func (s *Server) verifyTurnstile(ctx context.Context, token string) bool {
	if s.cfg.Development {
		return true
	}
	if len(token) == 0 || len(token) > 2048 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	form := url.Values{"secret": {s.cfg.TurnstileSecret}, "response": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.verifier.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Success  bool   `json:"success"`
		Hostname string `json:"hostname"`
		Action   string `json:"action"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result) != nil {
		return false
	}
	u, _ := url.Parse(s.cfg.Origin)
	return result.Success && result.Hostname == u.Hostname() && result.Action == "plant"
}
