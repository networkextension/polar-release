package release

// plugin.go — the polar-release service: a thin control plane over the
// assets data plane. It owns the `releases` registry (versions /
// channels / platforms → a content-addressed asset), mints
// upload/download grants through dock, and ed25519-signs every
// manifest. Bytes never pass through here.
//
// Three concerns, three owners (doc/arch/release-system.md §6):
//   bytes    → assets   (signed PUT/GET, replication)
//   registry → release  (this module)
//   identity → dock     (publisher auth via /internal/v1)

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	sdk "github.com/networkextension/polar-sdk"
)

// Config is assembled in cmd/release-svc/main.go from the environment.
type Config struct {
	DBDSN       string
	DockBase    string
	PluginName  string
	PluginToken string
	Listen       string
	Version      string
	SigningSeed  string // hex of ed25519 seed; empty → ephemeral + warn
	ApproverKeys string // comma-sep hex ed25519 PUBLIC keys trusted to stamp promotions
}

// Plugin holds the running service state.
type Plugin struct {
	DB        *sql.DB
	Dock      *sdk.Client
	Signer    *Signer
	Approvers *ApproverSet
	Name      string
	Listen    string
	Ver       string
	startedAt time.Time
}

// New opens the DB, ensures the schema, verifies dock connectivity, and
// loads (or generates) the signing key.
func New(ctx context.Context, cfg Config) (*Plugin, error) {
	db, err := sql.Open("postgres", cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	signer, err := NewSignerFromSeedHex(cfg.SigningSeed)
	if err != nil {
		if err != errNoSeed {
			_ = db.Close()
			return nil, err
		}
		signer, err = NewEphemeralSigner()
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		log.Printf("release: WARNING no POLAR_RELEASE_SIGNING_KEY — generated EPHEMERAL key (rotates every restart). pubkey=%s", signer.PublicKeyHex())
	} else {
		log.Printf("release: signing key loaded, pubkey=%s", signer.PublicKeyHex())
	}

	approvers := NewApproverSet(cfg.ApproverKeys)
	if approvers.Len() == 0 {
		log.Printf("release: WARNING no POLAR_RELEASE_APPROVER_KEYS — promotions into beta/stable will be refused until an approver pubkey is configured")
	} else {
		log.Printf("release: %d approver key(s) loaded (promotion stamps)", approvers.Len())
	}

	dock := sdk.NewClient(cfg.DockBase, cfg.PluginName, sdk.DeriveHMACKey(cfg.PluginToken))
	resp, err := dock.Do(http.MethodGet, "/internal/v1/ping", nil)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	resp.Body.Close()

	return &Plugin{
		DB: db, Dock: dock, Signer: signer, Approvers: approvers,
		Name: cfg.PluginName, Listen: cfg.Listen, Ver: cfg.Version,
		startedAt: time.Now(),
	}, nil
}

// RegisterRoutes wires the HTTP surface. Reads are public; writes sit
// behind the admin gate.
func (p *Plugin) RegisterRoutes(r gin.IRouter) {
	r.GET("/healthz", p.handleHealthz)

	rel := r.Group("/release")
	{
		// public read/distribution surface
		rel.GET("/pubkey", p.handlePubkey)
		rel.GET("/resolve", p.handleResolve)

		// admin write surface
		w := rel.Group("", p.requireAdminViaDock())
		w.POST("/publish", p.handlePublish)
		w.POST("/finalize", p.handleFinalize)
		w.POST("/yank", p.handleYank)
		w.GET("/list", p.handleList)
		// P2 promotion gate
		w.POST("/stage-result", p.handleStageResult)
		w.GET("/stage-run", p.handleStageRun)
		w.POST("/stamp", p.handleStamp)
		w.POST("/promote", p.handlePromote)
	}
}

func (p *Plugin) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "polar-release",
		"version": p.Ver,
		"uptime":  int64(time.Since(p.startedAt).Seconds()),
		"pubkey":  p.Signer.PublicKeyHex(),
	})
}

// Start kicks off the heartbeat loop.
func (p *Plugin) Start(ctx context.Context) { go p.heartbeatLoop(ctx) }

func (p *Plugin) heartbeatLoop(ctx context.Context) {
	p.beat()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.beat()
		}
	}
}

func (p *Plugin) beat() {
	_, err := p.Dock.HeartbeatV2(sdk.HeartbeatOpts{
		Version:       p.Ver,
		Endpoint:      p.Listen,
		UptimeSeconds: int64(time.Since(p.startedAt).Seconds()),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})
	if err != nil {
		log.Printf("release: heartbeat failed: %v", err)
	}
}
