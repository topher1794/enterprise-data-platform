-- 000009: audit — append-only record of every state-changing operation.
--
-- This table is deliberately append-only and unupdatable: a trigger rejects
-- UPDATE and DELETE so the trail cannot be rewritten by a compromised
-- application role. Retention is enforced by external archival, not deletion.

CREATE TABLE audit_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Monotonic per-second ordering key, independent of the UUID. Lets
    -- consumers page deterministically through a high-volume stream.
    sequence_no     BIGSERIAL   NOT NULL,

    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    actor_id        TEXT        NOT NULL DEFAULT '',
    actor_email     TEXT        NOT NULL DEFAULT '',
    actor_roles     TEXT[]      NOT NULL DEFAULT '{}',
    tenant_id       TEXT        NOT NULL DEFAULT '',

    -- action: source.created, pipeline.updated, dataset.deleted, ...
    action          TEXT        NOT NULL,

    -- entity_type: source, dataset, pipeline, quality_rule, policy, ...
    entity_type     TEXT        NOT NULL,
    entity_id       UUID,
    entity_label    TEXT        NOT NULL DEFAULT '',

    -- change_set holds before/after snapshots for updates and the payload for
    -- creates. Redacted at write time so secrets never enter the trail.
    change_set      JSONB       NOT NULL DEFAULT '{}'::jsonb,

    outcome         TEXT        NOT NULL DEFAULT 'success'
                    CHECK (outcome IN ('success', 'failure', 'denied')),

    ip_address      INET,
    user_agent      TEXT        NOT NULL DEFAULT '',
    request_id      TEXT        NOT NULL DEFAULT '',
    trace_id        TEXT        NOT NULL DEFAULT '',

    -- duration_ms records how long the audited operation took.
    duration_ms     INTEGER     CHECK (duration_ms IS NULL OR duration_ms >= 0)
);

-- Enforce append-only semantics.
CREATE OR REPLACE FUNCTION audit_events_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only; % is not permitted', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

-- Default "recent activity for an entity" and "recent activity by an actor".
CREATE INDEX idx_audit_events_time ON audit_events (occurred_at DESC);
CREATE INDEX idx_audit_events_entity ON audit_events (entity_type, entity_id, occurred_at DESC);
CREATE INDEX idx_audit_events_actor ON audit_events (actor_id, occurred_at DESC);
CREATE INDEX idx_audit_events_action ON audit_events (action, occurred_at DESC);
CREATE INDEX idx_audit_events_request ON audit_events (request_id) WHERE request_id <> '';
CREATE INDEX idx_audit_events_tenant ON audit_events (tenant_id, occurred_at DESC) WHERE tenant_id <> '';
-- Denied actions are rare and security-relevant; keep them cheap to query.
CREATE INDEX idx_audit_events_denied ON audit_events (occurred_at DESC) WHERE outcome = 'denied';
CREATE INDEX idx_audit_events_change_set ON audit_events USING GIN (change_set jsonb_path_ops);
