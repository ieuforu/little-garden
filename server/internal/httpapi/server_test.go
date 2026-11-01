package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/ieuforu/little-garden/server/internal/garden"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, err := garden.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := garden.New(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Development: true, Secret: bytes.Repeat([]byte{1}, 32), MaxConnections: 4}, engine, store, fstest.MapFS{"index.html": {Data: []byte("garden")}})
	server := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Shutdown(); server.Close(); store.Close() })
	return s, server
}
func httpCall(t *testing.T, server *httptest.Server, method, path, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}
func testSession(t *testing.T, server *httptest.Server) *http.Cookie {
	t.Helper()
	response := httpCall(t, server, "POST", "/api/v1/session", `{"token":""}`, server.URL, nil)
	if response.StatusCode != 201 {
		t.Fatalf("session status=%d", response.StatusCode)
	}
	return response.Cookies()[0]
}

func TestSecurityBoundaries(t *testing.T) {
	s, server := testServer(t)
	if status := httpCall(t, server, "POST", "/api/v1/session", `{}`, "https://evil.example", nil).StatusCode; status != 403 {
		t.Fatalf("cross-origin session: %d", status)
	}
	if status := httpCall(t, server, "POST", "/api/v1/session", `{}`, "", nil).StatusCode; status != 403 {
		t.Fatalf("missing origin: %d", status)
	}
	if status := httpCall(t, server, "POST", "/api/v1/flowers", `{}`, server.URL, nil).StatusCode; status != 401 {
		t.Fatalf("unauthenticated write: %d", status)
	}
	cookie := testSession(t, server)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	bad := *cookie
	bad.Value += "x"
	if status := httpCall(t, server, "POST", "/api/v1/flowers", `{}`, server.URL, &bad).StatusCode; status != 401 {
		t.Fatalf("forged cookie accepted: %d", status)
	}
	body := `{"gardenId":1,"requestId":"a-valid-request","x":1100,"y":950,"palette":1,"extra":"` + strings.Repeat("x", 3000) + `"}`
	if status := httpCall(t, server, "POST", "/api/v1/flowers", body, server.URL, cookie).StatusCode; status != 413 {
		t.Fatalf("oversize: %d", status)
	}
	if status := httpCall(t, server, "POST", "/api/v1/flowers", `{} {}`, server.URL, cookie).StatusCode; status != 400 {
		t.Fatalf("multiple JSON values: %d", status)
	}
	r := httptest.NewRequest("GET", "http://garden/", nil)
	r.RemoteAddr = "203.0.113.5:4444"
	r.Header.Set("X-Real-IP", "1.1.1.1")
	r.Header.Set("CF-Connecting-IP", "2.2.2.2")
	if got := s.clientIP(r); got != "203.0.113.5" {
		t.Fatalf("untrusted forwarded IP: %s", got)
	}
	s.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	r.RemoteAddr = "127.0.0.1:4444"
	if got := s.clientIP(r); got != "1.1.1.1" {
		t.Fatal("trusted proxy ignored")
	}
	r.RemoteAddr = "[2001:db8:1234:5678::5]:4444"
	if got := s.clientIP(r); got != "2001:db8:1234:5678::/64" {
		t.Fatal("IPv6 addresses not grouped")
	}
}

func TestTwoBrowsersReceiveSavedFlowerAndReconnect(t *testing.T) {
	_, server := testServer(t)
	cookie := testSession(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var snap garden.Snapshot
		if json.Unmarshal(data, &snap) != nil || snap.Type != "snapshot" {
			t.Fatal("snapshot must be first")
		}
		return c
	}
	a, b := dial(), dial()
	body := `{"gardenId":1,"requestId":"websocket-request","x":1100,"y":950,"palette":1}`
	response := httpCall(t, server, "POST", "/api/v1/flowers", body, server.URL, cookie)
	if response.StatusCode != 201 {
		t.Fatalf("plant status=%d", response.StatusCode)
	}
	var result garden.PlantResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*websocket.Conn{a, b} {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event garden.PlantResult
		_ = json.Unmarshal(data, &event)
		if event.Flower.ID != result.Flower.ID || event.Revision != 1 {
			t.Fatal("inconsistent broadcast")
		}
	}
	response = httpCall(t, server, "POST", "/api/v1/flowers", body, server.URL, cookie)
	if response.StatusCode != 200 {
		t.Fatal("retry not idempotent")
	}
	a.CloseNow()
	b.CloseNow()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot garden.Snapshot
	_ = json.Unmarshal(data, &snapshot)
	if len(snapshot.Flowers) != 17 || snapshot.Garden.Revision != 1 {
		t.Fatal("reconnect lost flowers")
	}
}

func TestWebsocketOriginAndConnectionBudgets(t *testing.T) {
	s, server := testServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	_, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	if err == nil || response.StatusCode != 403 {
		t.Fatal("cross-origin websocket accepted")
	}
	cookie := testSession(t, server)
	headers := http.Header{"Origin": {server.URL}, "Cookie": {cookie.String()}}
	var sockets []*websocket.Conn
	for i := 0; i < 2; i++ {
		c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			t.Fatal(err)
		}
		sockets = append(sockets, c)
		defer c.CloseNow()
		if _, _, err = c.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_, response, err = websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || response.StatusCode != 429 {
		t.Fatal("session connection budget bypassed")
	}
	if err = sockets[0].Write(ctx, websocket.MessageText, []byte("not an input protocol")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sockets[0].Read(ctx); err == nil {
		t.Fatal("accepted application messages on receive-only connection")
	}
	s.connections.mu.Lock()
	count := s.connections.count
	s.connections.mu.Unlock()
	if count > 2 {
		t.Fatal("rejected connection leaked permit")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTurnstileServerValidation(t *testing.T) {
	s, server := testServer(t)
	s.cfg.Development = false
	s.cfg.Origin = "https://garden.example"
	s.cfg.TurnstileSecret = "test-secret"
	var mu sync.Mutex
	used := make(map[string]bool)
	s.verifier = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("secret") != "test-secret" {
			t.Fatal("missing server secret")
		}
		token := r.Form.Get("response")
		mu.Lock()
		success := token != "forged" && !used[token]
		used[token] = true
		mu.Unlock()
		host := "garden.example"
		if token == "wrong-host" {
			host = "evil.example"
		}
		action := "plant"
		if token == "wrong-action" {
			action = "login"
		}
		data, _ := json.Marshal(map[string]any{"success": success, "hostname": host, "action": action})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	for _, token := range []string{"forged", "wrong-host", "wrong-action"} {
		if s.verifyTurnstile(context.Background(), token) {
			t.Fatalf("accepted %s", token)
		}
	}
	if !s.verifyTurnstile(context.Background(), "valid") || s.verifyTurnstile(context.Background(), "valid") {
		t.Fatal("replay handling")
	}
	response := httpCall(t, server, "POST", "/api/v1/session", `{"token":"new-valid"}`, "https://garden.example", nil)
	if response.StatusCode != 201 {
		t.Fatalf("session=%d", response.StatusCode)
	}
	cookie := response.Cookies()[0]
	if !cookie.Secure || cookie.Name != "__Host-garden_session" || cookie.Domain != "" {
		t.Fatal("production cookie flags")
	}
}

func TestLimitsRemainBoundedAndReturn429(t *testing.T) {
	l := newLimiter(2)
	if !l.allow("a", .01, 1) || l.allow("a", .01, 1) || !l.allow("b", .01, 1) || l.allow("c", .01, 1) {
		t.Fatal("bounded limiter failed")
	}
	_, server := testServer(t)
	cookie := testSession(t, server)
	var status int
	for i := 0; i < 4; i++ {
		response := httpCall(t, server, "POST", "/api/v1/flowers", `{"gardenId":1,"requestId":"same-request","x":1100,"y":950,"palette":1}`, server.URL, cookie)
		status = response.StatusCode
		if i == 3 && response.Header.Get("Retry-After") == "" {
			t.Fatal("no retry guidance")
		}
	}
	if status != 429 {
		t.Fatalf("rate limit bypassed: %d", status)
	}
}

func TestProductionConfigFailsClosed(t *testing.T) {
	for _, key := range []string{"GARDEN_MODE", "SESSION_SECRET", "PUBLIC_ORIGIN", "TURNSTILE_SITE_KEY", "TURNSTILE_SECRET_KEY", "TRUSTED_PROXIES"} {
		t.Setenv(key, "")
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("production started without credentials")
	}
	t.Setenv("GARDEN_MODE", "development")
	t.Setenv("LISTEN_ADDR", ":8080")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("development exposed publicly")
	}
	t.Setenv("LISTEN_ADDR", "127.0.0.1:8081")
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRUSTED_PROXIES", "0.0.0.0/0")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("trust-all proxy accepted")
	}
}
