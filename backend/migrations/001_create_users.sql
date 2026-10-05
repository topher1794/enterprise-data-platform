-- Migration: 001_create_users
-- Creates the users table with authentication fields.

-- Table: users
-- Stores user account information for authentication and authorization.

create table if not exists users (
    id uuid primary key default gen_random_uuid(),
    email text not null unique,
    password_hash text not null,
    first_name text not null default '',
    last_name text not null default '',
    status text not null default 'ACTIVE',
    email_verified boolean not null default false,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone not null default now(),
    updated_at timestamp with time zone not null default now()
);

-- Comment: status supports ACTIVE, INACTIVE, LOCKED, PENDING
-- Comment: email is stored normalized (lowercase, trimmed)
-- Comment: password_hash stores bcrypt/argon2 hash, never plaintext
-- Index: users_email_idx on lower((trim(email))) for case-insensitive search

create index if not exists users_email_idx on users (lower((trim(email))));