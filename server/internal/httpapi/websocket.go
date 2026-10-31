package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

func fmtInt(n int) string { return strconv.Itoa(n) }

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(r) {
		apiError(w, 403, "origin_denied", 0)
		return
	}
	ip := s.clientIP(r)
	if !s.limit.allow("ws:"+ip, 0.2, 5) || !s.limit.allow("ws:global", 5, 10) {
		apiError(w, 429, "rate_limited", 5)
		return
	}
	claims, _ := s.session(r)
	release, ok := s.connections.acquire(ip, claims.ID)
	if !ok {
		apiError(w, 429, "connection_limit", 10)
		return
	}
	defer release()
	// Exact scheme+host origin check was performed above, before accepting the upgrade.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1024)
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()
	// This is a receive-only stream. The library handles control frames and rejects app messages.
	ctx = c.CloseRead(ctx)
	sub := s.garden.Subscribe()
	defer s.garden.Unsubscribe(sub)
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-sub.Events:
			if !ok {
				return
			}
			deadline, done := context.WithTimeout(ctx, 5*time.Second)
			err = c.Write(deadline, websocket.MessageText, data)
			done()
			if err != nil {
				return
			}
		case <-ticker.C:
			deadline, done := context.WithTimeout(ctx, 5*time.Second)
			err = c.Ping(deadline)
			done()
			if err != nil {
				return
			}
		}
	}
}
