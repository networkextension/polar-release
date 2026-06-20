package release

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestHandleUI confirms the embedded portal serves at the root with the
// expected content type and a non-empty, recognizable HTML body.
func TestHandleUI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	p := &Plugin{}
	r.GET("/", p.handleUI)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html…", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"<title>Polar Release</title>", "/release/list", "/release/pubkey", "/release/promote"} {
		if !strings.Contains(body, want) {
			t.Errorf("portal body missing %q", want)
		}
	}
}
