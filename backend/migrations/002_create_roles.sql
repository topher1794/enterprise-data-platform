-- Migration: 002_create_roles
-- Creates the roles table and initial role data.

-- Table: roles
-- Stores system roles for RBAC.

create table if not exists roles (
    id uuid primary key default gen_random_uuid(),
    name text not null unique,
    description text not null default '',
    created_at timestamp with time zone not null default now()
);

-- Comment: role names: admin, data_engineer, data_analyst, viewer

-- Initial role data insert:
insert into roles (id, name, description) values
    (gen_random_uuid(), 'admin', 'Full administrative access'),
    (gen_random_uuid(), 'data_engineer', 'Data engineering and pipeline management'),
    (gen_random_uuid(), 'data_analyst', 'Data analysis and reporting'),
    (gen_random_uuid(), 'viewer', 'Read-only access to data and reports')
on conflict (name) do nothing;