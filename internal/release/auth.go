package release

// auth.go — publisher authentication. Writes (publish / finalize /
// yank) require a dock session whose role is owner/admin: only platform
// operators cut releases. Reads (resolve / pubkey) are public — they
// return a signed, short-lived provider URL + the manifest signature,
// which are the actual security boundary for distribution.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ctxUserID = "uid"
	ctxRole   = "role"
	ctxWS     = "ws"
)

func extractAccessToken(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("Authorization")); v != "" {
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	if v := strings.TrimSpace(c.Query("access_token")); v != "" {
		return v
	}
	if v, err := c.Cookie("access_token"); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// requireAdminViaDock verifies the session via dock and gates on an
// owner/admin role — the publisher gate.
func (p *Plugin) requireAdminViaDock() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := extractAccessToken(c)
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		res, err := p.Dock.AuthVerify(tok)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return
		}
		role := strings.ToLower(res.Role)
		if role != "owner" && role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "publishing requires an admin/owner session"})
			return
		}
		c.Set(ctxUserID, res.UserID)
		c.Set(ctxRole, role)
		c.Set(ctxWS, res.WorkspaceID)
		c.Next()
	}
}
