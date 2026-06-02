package release

// store.go — the releases registry. One Postgres table, owned by this
// module (POLAR_RELEASE_DB_DSN). artifact_id is the dock-side assets
// row id — a LOGICAL reference (assets live in dock's DB, so it can't be
// an enforced cross-DB FK). The bytes themselves never live here; this
// table is metadata + the ed25519 signature.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// schemaSQL is applied (idempotently) at boot — no manual migrate step.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS releases (
    id            BIGSERIAL PRIMARY KEY,
    module        TEXT NOT NULL,
    version       TEXT NOT NULL,
    channel       TEXT NOT NULL,
    platform      TEXT NOT NULL,
    artifact_id   BIGINT NOT NULL,            -- dock assets.id (logical ref)
    sha256        TEXT NOT NULL,
    ed25519_sig   TEXT NOT NULL,
    manifest      JSONB NOT NULL,
    published_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    yanked_at     TIMESTAMPTZ,
    UNIQUE (module, version, channel, platform)
);
-- resolve hot path: newest non-yanked per (module, channel, platform)
CREATE INDEX IF NOT EXISTS idx_releases_resolve
    ON releases (module, channel, platform, published_at DESC)
    WHERE yanked_at IS NULL;
`

// Release mirrors one row.
type Release struct {
	ID          int64      `json:"id"`
	Module      string     `json:"module"`
	Version     string     `json:"version"`
	Channel     string     `json:"channel"`
	Platform    string     `json:"platform"`
	ArtifactID  int64      `json:"artifact_id"`
	SHA256      string     `json:"sha256"`
	Ed25519Sig  string     `json:"ed25519_sig"`
	Manifest    Manifest   `json:"manifest"`
	PublishedAt time.Time  `json:"published_at"`
	YankedAt    *time.Time `json:"yanked_at,omitempty"`
}

// ensureSchema runs schemaSQL. Called once from New().
func ensureSchema(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	return err
}

// insertRelease commits a published release. On a (module,version,
// channel,platform) conflict it UPDATEs in place (re-publish of the
// same coordinates = overwrite, e.g. fixing a bad signature) and clears
// any prior yank.
func (p *Plugin) insertRelease(r *Release) error {
	mb, err := json.Marshal(r.Manifest)
	if err != nil {
		return err
	}
	return p.DB.QueryRow(`
		INSERT INTO releases
			(module, version, channel, platform, artifact_id, sha256, ed25519_sig, manifest, published_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb, now())
		ON CONFLICT (module, version, channel, platform) DO UPDATE
			SET artifact_id=EXCLUDED.artifact_id, sha256=EXCLUDED.sha256,
			    ed25519_sig=EXCLUDED.ed25519_sig, manifest=EXCLUDED.manifest,
			    published_at=now(), yanked_at=NULL
		RETURNING id, published_at`,
		r.Module, r.Version, r.Channel, r.Platform, r.ArtifactID,
		r.SHA256, r.Ed25519Sig, string(mb),
	).Scan(&r.ID, &r.PublishedAt)
}

// latestRelease returns the newest non-yanked release for the triple,
// or (nil, nil) on miss.
func (p *Plugin) latestRelease(module, channel, platform string) (*Release, error) {
	row := p.DB.QueryRow(`
		SELECT id, module, version, channel, platform, artifact_id, sha256,
		       ed25519_sig, manifest, published_at, yanked_at
		  FROM releases
		 WHERE module=$1 AND channel=$2 AND platform=$3 AND yanked_at IS NULL
		 ORDER BY published_at DESC
		 LIMIT 1`, module, channel, platform)
	return scanRelease(row)
}

// yankRelease soft-pulls a specific release. Returns sql.ErrNoRows-free
// (rowsAffected) so the handler can 404 on no-match.
func (p *Plugin) yankRelease(module, version, channel, platform string) (int64, error) {
	res, err := p.DB.Exec(`
		UPDATE releases SET yanked_at=now()
		 WHERE module=$1 AND version=$2 AND channel=$3 AND platform=$4 AND yanked_at IS NULL`,
		module, version, channel, platform)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// listReleases returns all rows for a module (newest first), optionally
// narrowed by channel/platform (empty = any). Admin/inspection use.
func (p *Plugin) listReleases(module, channel, platform string) ([]Release, error) {
	rows, err := p.DB.Query(`
		SELECT id, module, version, channel, platform, artifact_id, sha256,
		       ed25519_sig, manifest, published_at, yanked_at
		  FROM releases
		 WHERE module=$1
		   AND ($2='' OR channel=$2)
		   AND ($3='' OR platform=$3)
		 ORDER BY published_at DESC
		 LIMIT 500`, module, channel, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		r, err := scanReleaseRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanRelease(row *sql.Row) (*Release, error) {
	r, err := scanInto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func scanReleaseRows(rows *sql.Rows) (*Release, error) { return scanInto(rows) }

func scanInto(s scannable) (*Release, error) {
	var (
		r     Release
		mb    []byte
		yank  sql.NullTime
	)
	if err := s.Scan(&r.ID, &r.Module, &r.Version, &r.Channel, &r.Platform,
		&r.ArtifactID, &r.SHA256, &r.Ed25519Sig, &mb, &r.PublishedAt, &yank); err != nil {
		return nil, err
	}
	if len(mb) > 0 {
		_ = json.Unmarshal(mb, &r.Manifest)
	}
	if yank.Valid {
		r.YankedAt = &yank.Time
	}
	return &r, nil
}
