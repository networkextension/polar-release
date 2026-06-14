// Command release-svc is the polar-release control plane: the module
// publish/resolve registry on top of the assets data plane.
//
// Environment:
//
//	POLAR_DOCK_BASE            http://127.0.0.1:8080
//	POLAR_PLUGIN_NAME          release            (plugin_modules row)
//	POLAR_PLUGIN_TOKEN         polar_plugin_…      (plaintext)
//	POLAR_RELEASE_LISTEN       127.0.0.1:8101
//	POLAR_RELEASE_DB_DSN       postgres://…/polar_release?sslmode=disable
//	POLAR_RELEASE_SIGNING_KEY  hex of 32-byte ed25519 seed (openssl rand -hex 32)
//	POLAR_RELEASE_VERSION      cosmetic build tag
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/networkextension/polar-release/internal/release"
)

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := release.Config{
		DBDSN:       envOr("POLAR_RELEASE_DB_DSN", "postgres://ideamesh:test123456@127.0.0.1:5432/polar_release?sslmode=disable"),
		DockBase:    envOr("POLAR_DOCK_BASE", "http://127.0.0.1:8080"),
		PluginName:  envOr("POLAR_PLUGIN_NAME", "release"),
		PluginToken: strings.TrimSpace(os.Getenv("POLAR_PLUGIN_TOKEN")),
		Listen:      envOr("POLAR_RELEASE_LISTEN", "127.0.0.1:8101"),
		Version:      envOr("POLAR_RELEASE_VERSION", "0.0.1"),
		SigningSeed:  strings.TrimSpace(os.Getenv("POLAR_RELEASE_SIGNING_KEY")),
		ApproverKeys: strings.TrimSpace(os.Getenv("POLAR_RELEASE_APPROVER_KEYS")),
	}
	if cfg.PluginToken == "" {
		log.Fatal("POLAR_PLUGIN_TOKEN unset — mint one for the 'release' plugin in dock /admin-plugins.html")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	p, err := release.New(ctx, cfg)
	if err != nil {
		log.Fatalf("release: init: %v", err)
	}

	gin.SetMode(envOr("GIN_MODE", gin.ReleaseMode))
	r := gin.New()
	r.Use(gin.Recovery())
	p.RegisterRoutes(r)
	p.Start(ctx)

	srv := &http.Server{Addr: cfg.Listen, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("release: listening on %s (dock=%s, name=%s, ver=%s)", cfg.Listen, cfg.DockBase, cfg.PluginName, cfg.Version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("release: serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("release: shutdown")
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shCtx)
}
