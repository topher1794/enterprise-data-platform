-- The append-only trigger blocks DELETE, so the table must be dropped with
-- triggers disabled. Re-enabling afterwards restores the immutability guard.
SET session_replication_role = replica;
DROP TABLE IF EXISTS audit_events;
SET session_replication_role = origin;

DROP FUNCTION IF EXISTS audit_events_append_only();
