-- 000006: governance — access policies and data contracts.

CREATE TABLE governance_policies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',

    -- policy_type: access_control, retention, residency, consent, disclosure
    policy_type     TEXT        NOT NULL,
    effect          TEXT        NOT NULL DEFAULT 'deny' CHECK (effect IN ('allow', 'deny')),

    -- Rule expression evaluated by the authorization middleware. For example:
    --   subject.roles in ('data-steward') and resource.classification in ('restricted')
    rule_expression TEXT        NOT NULL,

    -- Minimum classification this policy applies to and above.
    applies_to_classification TEXT NOT NULL DEFAULT 'internal'
                    CHECK (applies_to_classification IN ('public', 'internal', 'confidential', 'restricted')),

    resource_type   TEXT        NOT NULL DEFAULT 'dataset'
                    CHECK (resource_type IN ('dataset', 'source', 'pipeline', 'column')),

    -- Optional scoping to a single dataset or source.
    resource_id     UUID,

    -- Automatic remediation when violated: none, quarantine, block, notify, delete
    remediation     TEXT        NOT NULL DEFAULT 'notify'
                    CHECK (remediation IN ('none', 'notify', 'quarantine', 'block', 'delete')),

    -- retention_days backs retention policies; 0 means no retention rule.
    retention_days  INTEGER     CHECK (retention_days IS NULL OR retention_days >= 0),

    priority        INTEGER     NOT NULL DEFAULT 100,
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,

    owner_id        TEXT        NOT NULL DEFAULT '',
    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT governance_policies_slug_unique UNIQUE (slug)
);

SELECT attach_updated_at_trigger('governance_policies');

CREATE INDEX idx_governance_policies_type ON governance_policies (policy_type) WHERE enabled AND deleted_at IS NULL;
CREATE INDEX idx_governance_policies_resource ON governance_policies (resource_type, resource_id)
    WHERE resource_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_governance_policies_priority ON governance_policies (priority) WHERE enabled AND deleted_at IS NULL;

CREATE TABLE data_contracts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id      UUID        NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    version         TEXT        NOT NULL DEFAULT '1.0.0',

    -- contract_status: draft, active, breached, deprecated
    status          TEXT        NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'active', 'breached', 'deprecated')),

    -- Producer guarantees: required columns, nullability, freshness SLA, volume.
    schema_definition JSONB     NOT NULL DEFAULT '{}'::jsonb,
    freshness_sla_minutes INTEGER CHECK (freshness_sla_minutes IS NULL OR freshness_sla_minutes > 0),
    min_row_count   BIGINT      CHECK (min_row_count IS NULL OR min_row_count >= 0),
    max_null_rate   NUMERIC(4,3) CHECK (max_null_rate IS NULL OR (max_null_rate >= 0 AND max_null_rate <= 1)),

    -- Consumer side of the agreement.
    consumer_teams  TEXT[]      NOT NULL DEFAULT '{}',
    breaking_change_policy TEXT NOT NULL DEFAULT 'notify'
                    CHECK (breaking_change_policy IN ('notify', 'major_version', 'block')),

    signed_at       TIMESTAMPTZ,
    signed_by       TEXT        NOT NULL DEFAULT '',

    owner_id        TEXT        NOT NULL DEFAULT '',
    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT data_contracts_dataset_version_unique UNIQUE (dataset_id, version)
);

SELECT attach_updated_at_trigger('data_contracts');

CREATE INDEX idx_data_contracts_dataset ON data_contracts (dataset_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_data_contracts_status ON data_contracts (status) WHERE deleted_at IS NULL;
