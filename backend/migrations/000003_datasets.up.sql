-- 000003: datasets — logical, governed data products published by the platform.

CREATE TABLE datasets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',

    -- dataset_kind: table, view, stream, file, api, ml_feature_set
    dataset_kind    TEXT        NOT NULL DEFAULT 'table'
                    CHECK (dataset_kind IN ('table', 'view', 'stream', 'file', 'api', 'ml_feature_set')),

    -- Classification drives governance: public, internal, confidential, restricted
    classification TEXT        NOT NULL DEFAULT 'internal'
                    CHECK (classification IN ('public', 'internal', 'confidential', 'restricted')),

    -- Reference to the physical landing location or table identifier.
    physical_location JSONB    NOT NULL DEFAULT '{}'::jsonb,

    -- Denormalised for cheap joins; the authoritative edges live in the
    -- lineage_edges table and are kept in sync by the lineage service.
    source_id       UUID        REFERENCES sources (id) ON DELETE RESTRICT,

    domain          TEXT        NOT NULL DEFAULT '',
    -- Retention window in days; 0 means retained indefinitely.
    retention_days  INTEGER     NOT NULL DEFAULT 0 CHECK (retention_days >= 0),

    -- schema_version increments on every breaking schema change.
    schema_version  INTEGER     NOT NULL DEFAULT 1 CHECK (schema_version > 0),

    owner_id        TEXT        NOT NULL,
    steward_id      TEXT        NOT NULL DEFAULT '',
    tags            TEXT[]      NOT NULL DEFAULT '{}',

    status          TEXT        NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'active', 'deprecated', 'archived')),

    -- Observed freshness/completeness, refreshed by the quality service.
    row_count       BIGINT,
    size_bytes      BIGINT,
    last_refreshed_at TIMESTAMPTZ,

    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT datasets_slug_unique UNIQUE (slug),
    CONSTRAINT datasets_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

SELECT attach_updated_at_trigger('datasets');

CREATE INDEX idx_datasets_status ON datasets (status) WHERE deleted_at IS NULL;
CREATE INDEX idx_datasets_source ON datasets (source_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_datasets_owner ON datasets (owner_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_datasets_steward ON datasets (steward_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_datasets_classification ON datasets (classification) WHERE deleted_at IS NULL;
CREATE INDEX idx_datasets_domain ON datasets (domain) WHERE deleted_at IS NULL;
CREATE INDEX idx_datasets_tags ON datasets USING GIN (tags);
CREATE INDEX idx_datasets_metadata ON datasets USING GIN (metadata jsonb_path_ops);

-- Dataset columns: the schema that quality rules and contracts validate against.
CREATE TABLE dataset_columns (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id      UUID        NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    ordinal         INTEGER     NOT NULL,
    data_type       TEXT        NOT NULL,
    is_nullable     BOOLEAN     NOT NULL DEFAULT TRUE,
    is_primary_key  BOOLEAN     NOT NULL DEFAULT FALSE,
    is_sensitive    BOOLEAN     NOT NULL DEFAULT FALSE,
    pii_class       TEXT        NOT NULL DEFAULT 'none'
                    CHECK (pii_class IN ('none', 'pii', 'phi', 'financial', 'credentials')),
    default_value   TEXT,
    description     TEXT        NOT NULL DEFAULT '',
    masking_strategy TEXT       NOT NULL DEFAULT 'none'
                    CHECK (masking_strategy IN ('none', 'hash', 'redact', 'tokenize', 'encrypt')),

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dataset_columns_name_unique UNIQUE (dataset_id, name),
    CONSTRAINT dataset_columns_ordinal_unique UNIQUE (dataset_id, ordinal)
);

SELECT attach_updated_at_trigger('dataset_columns');

CREATE INDEX idx_dataset_columns_dataset ON dataset_columns (dataset_id, ordinal);
CREATE INDEX idx_dataset_columns_sensitive ON dataset_columns (dataset_id) WHERE is_sensitive;
