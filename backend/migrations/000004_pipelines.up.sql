-- 000004: pipelines — directed acyclic graphs of tasks.
--
-- A pipeline owns its tasks; dependencies form the DAG edges. Cycle detection
-- is enforced in the pipeline validator before any write, and re-checked by a
-- deferred constraint trigger so concurrent edits cannot introduce a cycle.

CREATE TABLE pipelines (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',

    -- dag_id is the identifier published to Airflow; optional because a
    -- pipeline may be authored before it is deployed to an orchestrator.
    dag_id          TEXT,

    status          TEXT        NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'active', 'paused', 'archived')),

    -- schedule is a cron expression; empty means manual trigger only.
    schedule        TEXT        NOT NULL DEFAULT '',
    timezone        TEXT        NOT NULL DEFAULT 'UTC',

    -- Concurrency controls for orchestrated runs.
    max_active_runs INTEGER     NOT NULL DEFAULT 1 CHECK (max_active_runs >= 1),
    catchup         BOOLEAN     NOT NULL DEFAULT FALSE,

    default_dataset_id UUID    REFERENCES datasets (id) ON DELETE SET NULL,

    owner_id        TEXT        NOT NULL,
    tags            TEXT[]      NOT NULL DEFAULT '{}',

    -- Denormalised task count maintained by the pipeline service; avoids a
    -- count(*) on every list response.
    task_count      INTEGER     NOT NULL DEFAULT 0 CHECK (task_count >= 0),

    -- Validated graph hash: a change means the DAG shape changed, which lets
    -- the execution service detect drift between desired and deployed state.
    graph_hash      TEXT        NOT NULL DEFAULT '',

    metadata        JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT pipelines_slug_unique UNIQUE (slug),
    CONSTRAINT pipelines_dag_id_unique UNIQUE (dag_id),
    CONSTRAINT pipelines_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    -- A cron expression is either empty or exactly 5 fields.
    CONSTRAINT pipelines_schedule_format CHECK (schedule = '' OR schedule ~ '^[0-9*/,\-]+ +[0-9*/,\-]+ +[0-9*/,\-]+ +[0-9*/,\-]+ +[0-9*/,\-]+$')
);

SELECT attach_updated_at_trigger('pipelines');

CREATE INDEX idx_pipelines_status ON pipelines (status) WHERE deleted_at IS NULL;
CREATE INDEX idx_pipelines_owner ON pipelines (owner_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_pipelines_tags ON pipelines USING GIN (tags);
CREATE INDEX idx_pipelines_metadata ON pipelines USING GIN (metadata jsonb_path_ops);

CREATE TABLE pipeline_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID        NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,
    task_key        TEXT        NOT NULL,
    name            TEXT        NOT NULL,

    -- operator_type selects the executor: python, sql, bash, spark, http_poll,
    -- great_expectations, dbt, container, ...
    operator_type   TEXT        NOT NULL,
    operator_config JSONB       NOT NULL DEFAULT '{}'::jsonb,

    upstream_dataset_id UUID    REFERENCES datasets (id) ON DELETE SET NULL,
    downstream_dataset_id UUID REFERENCES datasets (id) ON DELETE SET NULL,

    -- Retry and timeout policy applied by the orchestrator.
    max_retries     INTEGER     NOT NULL DEFAULT 0 CHECK (max_retries >= 0),
    retry_delay_seconds INTEGER NOT NULL DEFAULT 300 CHECK (retry_delay_seconds >= 0),
    timeout_seconds INTEGER     NOT NULL DEFAULT 3600 CHECK (timeout_seconds > 0),

    -- SLAs surfaced on the operations dashboard.
    sla_minutes     INTEGER     CHECK (sla_minutes IS NULL OR sla_minutes > 0),

    is_critical     BOOLEAN     NOT NULL DEFAULT FALSE,
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT pipeline_tasks_key_unique UNIQUE (pipeline_id, task_key),
    CONSTRAINT pipeline_tasks_key_format CHECK (task_key ~ '^[a-z0-9]+(_[a-z0-9]+)*$')
);

SELECT attach_updated_at_trigger('pipeline_tasks');

CREATE INDEX idx_pipeline_tasks_pipeline ON pipeline_tasks (pipeline_id);
CREATE INDEX idx_pipeline_tasks_upstream_dataset ON pipeline_tasks (upstream_dataset_id);
CREATE INDEX idx_pipeline_tasks_downstream_dataset ON pipeline_tasks (downstream_dataset_id);
CREATE INDEX idx_pipeline_tasks_critical ON pipeline_tasks (pipeline_id) WHERE is_critical;

-- DAG edges. Both endpoints live in the same pipeline; the composite FK
-- guarantees it and provides the two indexes needed for cycle traversal.
CREATE TABLE pipeline_dependencies (
    pipeline_id     UUID        NOT NULL,
    upstream_key    TEXT        NOT NULL,
    downstream_key  TEXT        NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT pipeline_dependencies_pk PRIMARY KEY (pipeline_id, upstream_key, downstream_key),
    CONSTRAINT pipeline_dependencies_no_self_loop CHECK (upstream_key <> downstream_key),
    CONSTRAINT pipeline_dependencies_upstream_fk FOREIGN KEY (pipeline_id, upstream_key)
        REFERENCES pipeline_tasks (pipeline_id, task_key) ON DELETE CASCADE,
    CONSTRAINT pipeline_dependencies_downstream_fk FOREIGN KEY (pipeline_id, downstream_key)
        REFERENCES pipeline_tasks (pipeline_id, task_key) ON DELETE CASCADE
);

CREATE INDEX idx_pipeline_dependencies_downstream
    ON pipeline_dependencies (pipeline_id, downstream_key);
CREATE INDEX idx_pipeline_dependencies_upstream
    ON pipeline_dependencies (pipeline_id, upstream_key);
