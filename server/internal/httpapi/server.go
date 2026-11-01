package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ieuforu/little-garden/server/internal/garden"
)

type Server struct {
	cfg          Config
	garden       *garden.Engine
	store        *garden.Store
	limit        *limiter
	connections  connectionBudget
	verification chan struct{}
	requests     chan struct{}
	verifier     *http.Client
	static       fs.FS
	ctx          context.Context
	cancel       context.CancelFunc
}

func New(cfg Config, g *garden.Engine, store *garden.Store, static fs.FS) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{cfg: cfg, garden: g, store: store, limit: newLimiter(20000), connections: connectionBudget{max: cfg.MaxConnections, byIP: make(map[string]int), byVisitor: make(map[string]int)}, verification: make(chan struct{}, 8), requests: make(chan struct{}, 64), verifier: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, static: static, ctx: ctx, cancel: cancel}
}

func (s *Server) Shutdown() { s.cancel(); s.garden.Shutdown() }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if s.store.Ping(ctx) != nil {
			apiError(w, 503, "not_ready", 0)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/v1/garden", s.getGarden)
	mux.HandleFunc("GET /api/v1/session", s.getSession)
	mux.HandleFunc("POST /api/v1/session", s.createSession)
	mux.HandleFunc("POST /api/v1/flowers", s.plant)
	mux.HandleFunc("GET /api/v1/ws", s.websocket)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { apiError(w, 404, "not_found", 0) })
	mux.HandleFunc("/", s.serveStatic)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://challenges.cloudflare.com; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if !s.limit.allow("read:"+s.clientIP(r), 2, 60) || !s.limit.allow("read:global", 100, 200) {
				apiError(w, 429, "rate_limited", 3)
				return
			}
			if r.URL.Path != "/api/v1/ws" {
				select {
				case s.requests <- struct{}{}:
					defer func() { <-s.requests }()
				default:
					apiError(w, 503, "server_busy", 5)
					return
				}
			}
			if r.Method == http.MethodPost && !s.originOK(r) {
				apiError(w, 403, "origin_denied", 0)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) getGarden(w http.ResponseWriter, r *http.Request) {
	snapshot := s.garden.Snapshot()
	writeJSON(w, 200, struct {
		garden.Snapshot
		SiteKey     string `json:"siteKey"`
		Development bool   `json:"development"`
	}{snapshot, s.cfg.TurnstileSiteKey, s.cfg.Development})
}
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.session(r)
	writeJSON(w, 200, map[string]any{"authenticated": ok, "expiresAt": claims.Expires})
}
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	if claims, ok := s.session(r); ok {
		writeJSON(w, 200, map[string]any{"authenticated": true, "expiresAt": claims.Expires})
		return
	}
	ip := s.clientIP(r)
	if !s.limit.allow("session:"+ip, 1.0/60, 3) || !s.limit.allow("session:global", 1, 5) {
		apiError(w, 429, "rate_limited", 60)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, 4096, &req) {
		return
	}
	select {
	case s.verification <- struct{}{}:
		defer func() { <-s.verification }()
	default:
		apiError(w, 503, "verification_busy", 5)
		return
	}
	if !s.verifyTurnstile(r.Context(), req.Token) {
		apiError(w, 403, "verification_failed", 0)
		return
	}
	claims, err := s.issueSession(w)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"authenticated": true, "expiresAt": claims.Expires})
}
func (s *Server) plant(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.session(r)
	if !ok {
		apiError(w, 401, "session_required", 0)
		return
	}
	if !s.limit.allow("plant:visitor:"+claims.ID, 1.0/3, 3) || !s.limit.allow("plant:ip:"+s.clientIP(r), 2, 10) || !s.limit.allow("plant:global", 10, 20) {
		apiError(w, 429, "rate_limited", 3)
		return
	}
	var req garden.PlantRequest
	if !decodeJSON(w, r, 2048, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	result, err := s.garden.Plant(ctx, claims.ID, req)
	if err != nil {
		switch {
		case errors.Is(err, garden.ErrInvalid):
			apiError(w, 400, err.Error(), 0)
		case errors.Is(err, garden.ErrBanned):
			apiError(w, 403, err.Error(), 0)
		case errors.Is(err, garden.ErrFull), errors.Is(err, garden.ErrSpacing), errors.Is(err, garden.ErrPaused), errors.Is(err, garden.ErrStale), errors.Is(err, garden.ErrConflict):
			apiError(w, 409, err.Error(), 0)
		case errors.Is(err, garden.ErrBusy), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			apiError(w, 503, "garden_busy", 3)
		default:
			s.internalError(w, err)
		}
		return
	}
	status := 201
	if result.Duplicate {
		status = 200
	}
	writeJSON(w, status, result)
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if path == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(path, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	if !fs.ValidPath(path) || strings.HasPrefix(path, ".") || path == "placeholder.txt" {
		http.NotFound(w, r)
		return
	}
	if info, err := fs.Stat(s.static, path); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.FileServerFS(s.static).ServeHTTP(w, r)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		apiError(w, 415, "json_required", 0)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			apiError(w, 413, "body_too_large", 0)
		} else {
			apiError(w, 400, "invalid_json", 0)
		}
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		apiError(w, 400, "invalid_json", 0)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, code string, retry int) {
	if retry > 0 {
		w.Header().Set("Retry-After", fmtInt(retry))
	}
	writeJSON(w, status, map[string]any{"error": code, "retryAfter": retry})
}
func (s *Server) internalError(w http.ResponseWriter, err error) {
	// Bound error logs under an outage; never log bodies, cookies, tokens or client IPs.
	if s.limit.allow("error:log", 0.2, 2) {
		slog.Error("request failed", "error", err)
	}
	apiError(w, 503, "server_busy", 5)
}
