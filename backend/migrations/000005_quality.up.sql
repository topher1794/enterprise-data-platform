-- 000005: data quality — rules attached to datasets and their recorded results.

CREATE TABLE quality_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id      UUID        NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',

    -- rule_type: not_null, unique, range, regex, referential_integrity,
    -- freshness, volume, custom_sql, statistical
    rule_type       TEXT        NOT NULL,

    -- expectation follows the Great Expectations vocabulary
    -- (expect_column_values_to_not_be_null, expect_table_row_count_to_be_between, ...)
    expectation     TEXT        NOT NULL DEFAULT '',
    expectation_params JSONB    NOT NULL DEFAULT '{}'::jsonb,
    severity        TEXT        NOT NULL DEFAULT 'warning'
                    CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    -- Failure above this threshold fails the owning pipeline.
    failure_threshold NUMERIC(5,4) NOT NULL DEFAULT 0.0
                    CHECK (failure_threshold >= 0 AND failure_threshold <= 1),

    dimension       TEXT        NOT NULL DEFAULT 'accuracy'
                    CHECK (dimension IN ('accuracy', 'completeness', 'consistency', 'timeliness', 'uniqueness', 'validity', 'volume')),
    target_column   TEXT        NOT NULL DEFAULT '',
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    blocking        BOOLEAN     NOT NULL DEFAULT TRUE,

    -- Owning pipeline, when the rule gates a run.
    pipeline_id     UUID        REFERENCES pipelines (id) ON DELETE SET NULL,

    owner_id        TEXT        NOT NULL DEFAULT '',
    tags            TEXT[]      NOT NULL DEFAULT '{}',
    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT quality_rules_name_unique UNIQUE (dataset_id, name)
);

SELECT attach_updated_at_trigger('quality_rules');

CREATE INDEX idx_quality_rules_dataset ON quality_rules (dataset_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_quality_rules_pipeline ON quality_rules (pipeline_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_quality_rules_dimension ON quality_rules (dimension) WHERE deleted_at IS NULL;
CREATE INDEX idx_quality_rules_blocking ON quality_rules (dataset_id) WHERE enabled AND blocking AND deleted_at IS NULL;

-- Append-only record of one rule evaluation. Partitioned by month in a later
-- migration once retention volumes justify it.
CREATE TABLE quality_check_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id         UUID        NOT NULL REFERENCES quality_rules (id) ON DELETE CASCADE,
    dataset_id      UUID        NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    pipeline_run_id UUID,

    status          TEXT        NOT NULL
                    CHECK (status IN ('passed', 'warning', 'failed', 'error', 'skipped')),
    -- observed statistics backing the verdict.
    observed_value  NUMERIC,
    expected_value  TEXT        NOT NULL DEFAULT '',
    rows_evaluated  BIGINT,
    -- Fraction of evaluated rows that violated the rule, 0.0 to 1.0.
    violation_rate  NUMERIC(6,4),
    passed_count    BIGINT      NOT NULL DEFAULT 0,
    failed_count    BIGINT      NOT NULL DEFAULT 0,

    message         TEXT        NOT NULL DEFAULT '',
    details         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    duration_ms     INTEGER     CHECK (duration_ms IS NULL OR duration_ms >= 0),

    evaluated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_quality_check_runs_rule_time ON quality_check_runs (rule_id, evaluated_at DESC);
CREATE INDEX idx_quality_check_runs_dataset_time ON quality_check_runs (dataset_id, evaluated_at DESC);
CREATE INDEX idx_quality_check_runs_status ON quality_check_runs (status, evaluated_at DESC);
CREATE INDEX idx_quality_check_runs_run ON quality_check_runs (pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;
