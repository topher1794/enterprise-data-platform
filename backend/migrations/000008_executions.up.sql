-- 000008: execution — orchestrated pipeline and task runs.

CREATE TABLE pipeline_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID        NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,

    -- Orchestrator identifiers, present once Airflow has accepted the run.
    run_id          UUID        NOT NULL DEFAULT gen_random_uuid(),
    dag_run_id      TEXT,

    -- run_status: queued, running, success, failed, cancelled, skipped
    status          TEXT        NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'running', 'success', 'failed', 'cancelled', 'skipped')),

    -- trigger_type: manual, scheduled, api, upstream, backfill
    trigger_type    TEXT        NOT NULL DEFAULT 'manual'
                    CHECK (trigger_type IN ('manual', 'scheduled', 'api', 'upstream', 'backfill')),
    triggered_by    TEXT        NOT NULL DEFAULT '',

    -- run_config carries run-time parameters resolved from the pipeline defaults.
    run_config      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    params          JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- Logical date requested, versus actual start and end.
    logical_date    TIMESTAMPTZ,
    queued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,

    duration_ms     BIGINT      CHECK (duration_ms IS NULL OR duration_ms >= 0),

    task_count      INTEGER     NOT NULL DEFAULT 0 CHECK (task_count >= 0),
    succeeded_count INTEGER     NOT NULL DEFAULT 0 CHECK (succeeded_count >= 0),
    failed_count    INTEGER     NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    skipped_count   INTEGER     NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),

    -- Whether a blocking quality gate failed, distinct from an execution error.
    quality_gate_passed BOOLEAN,
    error_message   TEXT        NOT NULL DEFAULT '',
    logs_url        TEXT        NOT NULL DEFAULT '',

    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

SELECT attach_updated_at_trigger('pipeline_runs');

CREATE INDEX idx_pipeline_runs_pipeline_time ON pipeline_runs (pipeline_id, created_at DESC);
CREATE INDEX idx_pipeline_runs_status_time ON pipeline_runs (status, created_at DESC);
CREATE INDEX idx_pipeline_runs_run_id ON pipeline_runs (run_id);
CREATE INDEX idx_pipeline_runs_dag_run_id ON pipeline_runs (dag_run_id) WHERE dag_run_id IS NOT NULL;
CREATE INDEX idx_pipeline_runs_active ON pipeline_runs (pipeline_id) WHERE status IN ('queued', 'running');
-- Supports the operational "recent failures" view.
CREATE INDEX idx_pipeline_runs_failures ON pipeline_runs (pipeline_id, finished_at DESC) WHERE status = 'failed';

CREATE TABLE task_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_run_id UUID        NOT NULL REFERENCES pipeline_runs (id) ON DELETE CASCADE,
    task_id         UUID        REFERENCES pipeline_tasks (id) ON DELETE SET NULL,
    task_key        TEXT        NOT NULL,
    try_number      INTEGER     NOT NULL DEFAULT 1 CHECK (try_number >= 1),

    status          TEXT        NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'running', 'success', 'failed', 'retrying', 'skipped', 'upstream_failed')),
    operator_type   TEXT        NOT NULL DEFAULT '',
    attempt_started_at  TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    duration_ms     BIGINT      CHECK (duration_ms IS NULL OR duration_ms >= 0),
    exit_code       INTEGER,
    operator_output TEXT        NOT NULL DEFAULT '',
    error_message   TEXT        NOT NULL DEFAULT '',
    logs_url        TEXT        NOT NULL DEFAULT '',

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT task_runs_unique_attempt UNIQUE (pipeline_run_id, task_key, try_number)
);

SELECT attach_updated_at_trigger('task_runs');

CREATE INDEX idx_task_runs_pipeline_run ON task_runs (pipeline_run_id);
CREATE INDEX idx_task_runs_task ON task_runs (task_key, created_at DESC);
CREATE INDEX idx_task_runs_status ON task_runs (status, created_at DESC);
