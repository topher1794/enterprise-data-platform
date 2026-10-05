-- 000002: data sources — the upstream systems the platform ingests from.

CREATE TABLE sources (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',

    -- source_type enumerates the connector family: postgres, mysql, s3,
    -- kafka, bigquery, snowflake, salesforce, api, sap, sharepoint, ...
    source_type     TEXT        NOT NULL,

    -- connection_secret_ref is an opaque pointer into the secret store
    -- (Vault path or Secrets Manager ARN). Credentials are never stored here.
    connection_secret_ref TEXT  NOT NULL,

    -- connection_config holds non-sensitive connection metadata only.
    connection_config JSONB   NOT NULL DEFAULT '{}'::jsonb,

    environment     TEXT        NOT NULL DEFAULT 'production'
                    CHECK (environment IN ('development', 'staging', 'production')),

    -- status lifecycle: draft -> active -> degraded -> retired
    status          TEXT        NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'active', 'degraded', 'retired')),

    -- ingestion_mode: batch, streaming, or both
    ingestion_mode  TEXT        NOT NULL DEFAULT 'batch'
                    CHECK (ingestion_mode IN ('batch', 'streaming', 'both')),

    owner_id        TEXT        NOT NULL,
    tags            TEXT[]      NOT NULL DEFAULT '{}',

    -- Last successful connectivity probe, maintained by the health checker.
    last_health_check_at   TIMESTAMPTZ,
    last_health_check_status TEXT CHECK (last_health_check_status IN ('healthy', 'degraded', 'unhealthy')),

    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT sources_slug_unique UNIQUE (slug),
    CONSTRAINT sources_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

SELECT attach_updated_at_trigger('sources');

-- Supports the default "list active sources, newest first" query.
CREATE INDEX idx_sources_status_created_at ON sources (status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_sources_type ON sources (source_type) WHERE deleted_at IS NULL;
CREATE INDEX idx_sources_owner ON sources (owner_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_sources_tags ON sources USING GIN (tags);
CREATE INDEX idx_sources_metadata ON sources USING GIN (metadata jsonb_path_ops);
CREATE INDEX idx_sources_name_search ON sources USING GIN (to_tsvector('simple', name || ' ' || description));
