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
