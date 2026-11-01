package httpapi

import (
	"io"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ieuforu/little-garden/server/internal/assets"
)

func TestEmbeddedFrontendRouting(t *testing.T) {
	files := assets.Files()
	if _, err := fs.Stat(files, "index.html"); err != nil {
		t.Skip("run pnpm build:web to test the production embed")
	}
	api, _ := testServer(t)
	api.static = files
	for _, tc := range []struct {
		path     string
		status   int
		fragment string
	}{
		{"/", 200, "Little Garden"}, {"/flower.svg", 200, "<svg"}, {"/placeholder.txt", 404, "404"}, {"/does-not-exist.js", 404, "404"}, {"/api/missing", 404, "not_found"},
	} {
		recorder := httptest.NewRecorder()
		api.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", tc.path, nil))
		response := recorder.Result()
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != tc.status || !strings.Contains(string(body), tc.fragment) {
			t.Fatalf("%s returned %d %s", tc.path, response.StatusCode, body)
		}
	}
}
