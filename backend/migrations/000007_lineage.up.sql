-- 000007: lineage — column- and table-level provenance across sources,
-- datasets and pipelines.
--
-- Edges are stored as nodes rather than a self-referencing table so that both
-- endpoints may belong to any entity type.

CREATE TABLE lineage_edges (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Upstream (producer) endpoint.
    from_entity_type    TEXT        NOT NULL
                        CHECK (from_entity_type IN ('source', 'dataset', 'pipeline', 'task')),
    from_entity_id      UUID        NOT NULL,
    from_column         TEXT        NOT NULL DEFAULT '',

    -- Downstream (consumer) endpoint.
    to_entity_type      TEXT        NOT NULL
                        CHECK (to_entity_type IN ('source', 'dataset', 'pipeline', 'task')),
    to_entity_id        UUID        NOT NULL,
    to_column           TEXT        NOT NULL DEFAULT '',

    -- transform_type: copy, aggregate, join, filter, derive, union, enrich, ml_transform
    transform_type      TEXT        NOT NULL DEFAULT 'copy',
    transform_expression TEXT       NOT NULL DEFAULT '',

    -- lineage_confidence: inferred from query parsing, or declared by the owner.
    confidence          NUMERIC(3,2) NOT NULL DEFAULT 1.00
                        CHECK (confidence >= 0 AND confidence <= 1),

    -- Recorded provenance of the edge itself, for trust assessment.
    observed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    observed_by_run_id  UUID,

    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One row per endpoint pair and column pair; re-observation updates the
    -- existing edge rather than duplicating it.
    CONSTRAINT lineage_edges_unique UNIQUE (
        from_entity_type, from_entity_id, from_column,
        to_entity_type, to_entity_id, to_column
    ),
    CONSTRAINT lineage_edges_not_self CHECK (
        from_entity_type <> to_entity_type
        OR from_entity_id <> to_entity_id
        OR from_column <> to_column
    )
);

SELECT attach_updated_at_trigger('lineage_edges');

-- Primary traversal: everything downstream of a given entity.
CREATE INDEX idx_lineage_from ON lineage_edges (from_entity_type, from_entity_id);
-- Primary traversal: everything upstream of a given entity.
CREATE INDEX idx_lineage_to ON lineage_edges (to_entity_type, to_entity_id);
CREATE INDEX idx_lineage_column_level ON lineage_edges (from_entity_id, from_column)
    WHERE from_column <> '';
CREATE INDEX idx_lineage_observed ON lineage_edges (observed_at DESC);
CREATE INDEX idx_lineage_metadata ON lineage_edges USING GIN (metadata jsonb_path_ops);
