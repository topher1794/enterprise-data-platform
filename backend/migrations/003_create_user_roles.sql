-- Migration: 003_create_user_roles
-- Creates the user_roles junction table with unique constraint.

-- Table: user_roles
-- Maps users to their roles.

create table if not exists user_roles (
    user_id uuid not null,
    role_id uuid not null,
    created_at timestamp with time zone not null default now(),
    primary key (user_id, role_id)
);

-- Comment: unique constraint on (user_id, role_id) ensures a user cannot have
-- the same role assigned twice.

-- Add foreign key constraints (if not already present):
alter table if exists user_roles
    add constraint if not exists fk_user_roles_user
    foreign key (user_id) references users (id) on delete cascade;

alter table if exists user_roles
    add constraint if not exists fk_user_roles_role
    foreign key (role_id) references roles (id) on delete cascade;