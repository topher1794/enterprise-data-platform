-- Migration: 005_create_audit_logs
-- Creates the audit_logs table for authentication and general audit trail.

-- Table: audit_logs
-- Stores audit records for security and compliance.

create table if not exists audit_logs (
    id uuid primary key default gen_random_uuid(),
    user_id uuid,
    action text not null,
    resource text not null,
    resource_id uuid,
    ip_address inet,
    user_agent text,
    metadata jsonb not null default '{}'::jsonb,
    created_at timestamp with time zone not null default now()
);

-- Comment: action values include LOGIN_SUCCESS, LOGIN_FAILED, LOGOUT,
-- TOKEN_REFRESH, ACCOUNT_LOCKED, PASSWORD_CHANGED
-- Comment: resource typically is "authentication" or "user"
-- Comment: metadata is a JSONB field for additional context
-- Comment: ip_address stores the client IP address
-- Comment: user_agent stores the HTTP user agent string

-- Index: audit_logs_user_id_idx on user_id for user-specific queries.
-- Index: audit_logs_created_at_idx on created_at for time-based queries.

create index if not exists audit_logs_user_id_idx on audit_logs (user_id);
create index if not exists audit_logs_created_at_idx on audit_logs (created_at);