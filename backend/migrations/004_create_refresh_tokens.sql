-- Migration: 004_create_refresh_tokens
-- Creates the refresh_tokens table. Never stores raw refresh tokens.

-- Table: refresh_tokens
-- Stores hashed refresh tokens for token rotation and revocation.

create table if not exists refresh_tokens (
    id uuid primary key default gen_random_uuid(),
    user_id uuid not null,
    token_hash text not null,
    expires_at timestamp with time zone not null,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone not null default now(),
    last_used_at timestamp with time zone,
    user_agent text,
    ip_address inet
);

-- Comment: token_hash stores a cryptographic hash (bcrypt/argon2) of the raw token.
-- Comment: raw refresh token is never stored in the database.
-- Comment: last_used_at tracks the most recent token usage for abuse detection.

-- Index: refresh_tokens_user_id_idx on user_id for revocation queries.
-- Index: refresh_tokens_expires_at_idx on expires_at for expiration checks.

create index if not exists refresh_tokens_user_id_idx on refresh_tokens (user_id);
create index if not exists refresh_tokens_expires_at_idx on refresh_tokens (expires_at);