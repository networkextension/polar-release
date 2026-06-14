-- polar-release schema. Applied idempotently at boot by the service
-- (internal/release/store.go schemaSQL), so this file is for reference
-- and for bootstrapping a fresh DB by hand:
--
--   createdb -O ideamesh polar_release
--   psql -d polar_release -f scripts/migrate/release-schema.sql
--
-- artifact_id is the dock-side assets.id (a logical reference — the
-- assets catalog lives in dock's DB, so it is NOT an enforced cross-DB
-- foreign key). The bytes never live here; this table is metadata + the
-- ed25519 manifest signature.

CREATE TABLE IF NOT EXISTS releases (
    id            BIGSERIAL PRIMARY KEY,
    module        TEXT NOT NULL,
    version       TEXT NOT NULL,
    channel       TEXT NOT NULL,                 -- stable | beta | dev
    platform      TEXT NOT NULL,                 -- darwin-arm64 | ios-arm64 | …
    artifact_id   BIGINT NOT NULL,               -- dock assets.id (logical ref)
    sha256        TEXT NOT NULL,                 -- = the asset's content hash
    ed25519_sig   TEXT NOT NULL,                 -- signature over the manifest
    manifest      JSONB NOT NULL,
    published_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    yanked_at     TIMESTAMPTZ,
    UNIQUE (module, version, channel, platform)
);

CREATE INDEX IF NOT EXISTS idx_releases_resolve
    ON releases (module, channel, platform, published_at DESC)
    WHERE yanked_at IS NULL;

-- P2 promotion gate (doc/arch/zone-federation-platform.md §3): a version
-- must pass the stage zone (stage_runs) AND carry a signed stamp
-- (promotion_attestations) before promote into beta/stable. Channels:
-- dev (publish entry) → stage (test) → beta → stable.
CREATE TABLE IF NOT EXISTS stage_runs (
    id            BIGSERIAL PRIMARY KEY,
    module        TEXT NOT NULL,
    version       TEXT NOT NULL,
    platform      TEXT NOT NULL,
    passed        BOOLEAN NOT NULL,
    migrations_ok BOOLEAN NOT NULL DEFAULT FALSE,
    destructive   BOOLEAN NOT NULL DEFAULT FALSE,
    backup_ref    TEXT NOT NULL DEFAULT '',
    result        JSONB NOT NULL DEFAULT '{}',
    result_sha256 TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_stage_runs_lookup
    ON stage_runs (module, version, platform, created_at DESC);

CREATE TABLE IF NOT EXISTS promotion_attestations (
    id            BIGSERIAL PRIMARY KEY,
    module        TEXT NOT NULL,
    version       TEXT NOT NULL,
    platform      TEXT NOT NULL,
    from_channel  TEXT NOT NULL,
    to_channel    TEXT NOT NULL,
    stage_run_id  BIGINT NOT NULL REFERENCES stage_runs(id),
    result_sha256 TEXT NOT NULL,
    approver      TEXT NOT NULL,
    approver_kind TEXT NOT NULL,                   -- human | ai
    signature     TEXT NOT NULL,                   -- ed25519 hex over attestation canonical
    approved_at   TEXT NOT NULL,                   -- exact signed RFC3339 string
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_attestations_lookup
    ON promotion_attestations (module, version, platform, to_channel, created_at DESC);
