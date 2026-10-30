-- Account statuses and presence activities, moved out of Passport.
-- Column names, types, nullability and index names are copied verbatim from
-- DysonNetwork.Passport/Migrations/AppDatabaseModelSnapshot.cs (the
-- account_statuses, presence_activities, presence_activity_tags,
-- presence_catalog_items and presence_catalog_tags entities), so the Go
-- service and the remaining C# services share the same tables.
-- Idempotent: every statement is guarded with IF NOT EXISTS; the live fleet
-- database already holds these tables (created by EF), where this is a no-op.

-- ---------------------------------------------------------------------------
-- account_statuses
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS account_statuses (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    app_identifier varchar(4096) NULL,
    attitude integer NOT NULL,
    background jsonb NULL,
    cleared_at timestamp with time zone NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    icon jsonb NULL,
    is_automated boolean NOT NULL,
    label varchar(1024) NULL,
    meta jsonb NULL,
    symbol varchar(128) NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_account_statuses PRIMARY KEY (id)
);

-- ---------------------------------------------------------------------------
-- presence_activities
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS presence_activities (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    caption varchar(4096) NULL,
    catalog_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    ended_at timestamp with time zone NULL,
    large_image varchar(4096) NULL,
    lease_expires_at timestamp with time zone NOT NULL,
    lease_minutes integer NOT NULL,
    manual_id varchar(4096) NULL,
    meta jsonb NULL,
    provider varchar(256) NULL,
    queryable_terms jsonb NOT NULL,
    reference_id varchar(1024) NULL,
    small_image varchar(4096) NULL,
    started_at timestamp with time zone NULL,
    subtitle varchar(4096) NULL,
    subtitle_url varchar(4096) NULL,
    title varchar(4096) NULL,
    title_url varchar(4096) NULL,
    type text NULL,
    updated_at timestamp with time zone NOT NULL,
    visibility integer NOT NULL,
    CONSTRAINT pk_presence_activities PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS ix_presence_activities_account_id_provider_deleted_at
    ON presence_activities (account_id, provider, deleted_at);
CREATE INDEX IF NOT EXISTS ix_presence_activities_provider_reference_id_deleted_at
    ON presence_activities (provider, reference_id, deleted_at);
CREATE INDEX IF NOT EXISTS ix_presence_activities_account_id_started_at_ended_at_deleted_
    ON presence_activities (account_id, started_at, ended_at, deleted_at);

-- ---------------------------------------------------------------------------
-- presence_activity_tags
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS presence_activity_tags (
    activity_id uuid NOT NULL,
    slug varchar(256) NOT NULL,
    name varchar(256) NULL,
    CONSTRAINT pk_presence_activity_tags PRIMARY KEY (activity_id, slug),
    CONSTRAINT fk_presence_activity_tags_presence_activities_activity_id
        FOREIGN KEY (activity_id) REFERENCES presence_activities (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_presence_activity_tags_slug ON presence_activity_tags (slug);

-- ---------------------------------------------------------------------------
-- presence_catalog_items
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS presence_catalog_items (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    caption varchar(4096) NULL,
    catalog_key varchar(1024) NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    large_image varchar(4096) NULL,
    last_active_at timestamp with time zone NULL,
    meta jsonb NOT NULL,
    name varchar(4096) NULL,
    provider varchar(256) NULL,
    queryable_terms jsonb NOT NULL,
    reference_id varchar(1024) NULL,
    session_count integer NOT NULL,
    small_image varchar(4096) NULL,
    subtitle varchar(4096) NULL,
    subtitle_url varchar(4096) NULL,
    title_url varchar(4096) NULL,
    total_seconds bigint NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    visibility integer NOT NULL,
    CONSTRAINT pk_presence_catalog_items PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS ix_presence_catalog_items_account_id_deleted_at
    ON presence_catalog_items (account_id, deleted_at);
CREATE INDEX IF NOT EXISTS ix_presence_catalog_items_account_id_provider_catalog_key_dele
    ON presence_catalog_items (account_id, provider, catalog_key, deleted_at);

-- ---------------------------------------------------------------------------
-- presence_catalog_tags
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS presence_catalog_tags (
    catalog_id uuid NOT NULL,
    slug varchar(256) NOT NULL,
    name varchar(256) NULL,
    CONSTRAINT pk_presence_catalog_tags PRIMARY KEY (catalog_id, slug),
    CONSTRAINT fk_presence_catalog_tags_presence_catalog_items_catalog_id
        FOREIGN KEY (catalog_id) REFERENCES presence_catalog_items (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_presence_catalog_tags_slug ON presence_catalog_tags (slug);
