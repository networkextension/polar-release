package release

// ui.go — the self-contained release portal. polar-release has no node
// build toolchain, so the page is a single hand-written HTML+vanilla-JS
// file embedded into the binary and served at the service root.
//
// It calls the same-origin /release/* API. The admin write surface
// (yank/promote) is gated by requireAdminViaDock, which accepts the
// `access_token` cookie — so when dock runs with POLAR_COOKIE_DOMAIN set
// (cross-subdomain SSO, e.g. .4950.store), a logged-in operator visiting
// release.<domain> is authenticated with zero CORS/proxy glue.
//
// The portal registers itself into the shared platform sidebar via the
// UIRoutes carried on the SDK heartbeat (see beat()).

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed ui/index.html
var uiFS embed.FS

// handleUI serves the portal HTML at the service root.
func (p *Plugin) handleUI(c *gin.Context) {
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "ui unavailable")
		return
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", b)
}
