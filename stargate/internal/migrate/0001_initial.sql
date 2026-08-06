-- Stargate initial schema.
-- Generated from the EF Core model snapshots:
--   DysonNetwork.Padlock/Migrations/AppDatabaseModelSnapshot.cs  (all 23 tables)
--   DysonNetwork.Passport/Migrations/AppDatabaseModelSnapshot.cs (account_profiles, account_relationships)
-- plus the trigram search indexes from 20260521152351_AddAccountTrigramSearch.cs.
-- Naming follows EF Core's snake_case convention; instant columns are timestamp with time zone.
-- Id columns have no DEFAULT: the application generates UUIDs client-side.
-- Idempotent: every statement is guarded with DROP ... IF EXISTS.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------------------------------------------------------------------------
-- accounts
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS accounts CASCADE;
CREATE TABLE accounts (
    id uuid NOT NULL,
    activated_at timestamp with time zone NULL,
    automated_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    is_superuser boolean NOT NULL,
    language varchar(32) NOT NULL,
    name varchar(256) NOT NULL,
    nick varchar(256) NOT NULL,
    region varchar(32) NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_accounts PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_accounts_name_deleted_at;
CREATE UNIQUE INDEX ix_accounts_name_deleted_at ON accounts (name, deleted_at);

DROP INDEX IF EXISTS ix_accounts_name_trgm;
CREATE INDEX ix_accounts_name_trgm ON accounts USING gin (name gin_trgm_ops) WHERE deleted_at IS NULL AND name IS NOT NULL;

DROP INDEX IF EXISTS ix_accounts_nick_trgm;
CREATE INDEX ix_accounts_nick_trgm ON accounts USING gin (nick gin_trgm_ops) WHERE deleted_at IS NULL AND nick IS NOT NULL;

-- ---------------------------------------------------------------------------
-- permission_groups
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS permission_groups CASCADE;
CREATE TABLE permission_groups (
    id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    "key" varchar(1024) NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_permission_groups PRIMARY KEY (id)
);

-- ---------------------------------------------------------------------------
-- auth_clients
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS auth_clients CASCADE;
CREATE TABLE auth_clients (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(1024) NOT NULL,
    device_label varchar(1024) NULL,
    device_name varchar(1024) NOT NULL,
    platform integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_auth_clients PRIMARY KEY (id),
    CONSTRAINT fk_auth_clients_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_auth_clients_account_id_device_id_deleted_at;
CREATE UNIQUE INDEX ix_auth_clients_account_id_device_id_deleted_at ON auth_clients (account_id, device_id, deleted_at);

-- ---------------------------------------------------------------------------
-- auth_sessions
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS auth_sessions CASCADE;
CREATE TABLE auth_sessions (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    app_id uuid NULL,
    audiences jsonb NOT NULL,
    challenge_id uuid NULL,
    client_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    epoch integer NOT NULL,
    expired_at timestamp with time zone NULL,
    ip_address varchar(128) NULL,
    last_granted_at timestamp with time zone NULL,
    location jsonb NULL,
    parent_session_id uuid NULL,
    scopes jsonb NOT NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    user_agent varchar(512) NULL,
    CONSTRAINT pk_auth_sessions PRIMARY KEY (id),
    CONSTRAINT fk_auth_sessions_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE,
    CONSTRAINT fk_auth_sessions_auth_clients_client_id FOREIGN KEY (client_id) REFERENCES auth_clients (id),
    CONSTRAINT fk_auth_sessions_auth_sessions_parent_session_id FOREIGN KEY (parent_session_id) REFERENCES auth_sessions (id)
);

DROP INDEX IF EXISTS ix_auth_sessions_account_id;
CREATE INDEX ix_auth_sessions_account_id ON auth_sessions (account_id);

DROP INDEX IF EXISTS ix_auth_sessions_client_id;
CREATE INDEX ix_auth_sessions_client_id ON auth_sessions (client_id);

DROP INDEX IF EXISTS ix_auth_sessions_parent_session_id;
CREATE INDEX ix_auth_sessions_parent_session_id ON auth_sessions (parent_session_id);

-- ---------------------------------------------------------------------------
-- api_keys
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS api_keys CASCADE;
CREATE TABLE api_keys (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    app_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    label varchar(1024) NOT NULL,
    session_id uuid NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_api_keys PRIMARY KEY (id),
    CONSTRAINT fk_api_keys_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE,
    CONSTRAINT fk_api_keys_auth_sessions_session_id FOREIGN KEY (session_id) REFERENCES auth_sessions (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_api_keys_account_id;
CREATE INDEX ix_api_keys_account_id ON api_keys (account_id);

DROP INDEX IF EXISTS ix_api_keys_session_id;
CREATE INDEX ix_api_keys_session_id ON api_keys (session_id);

-- ---------------------------------------------------------------------------
-- account_auth_factors
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS account_auth_factors CASCADE;
CREATE TABLE account_auth_factors (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    config jsonb NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    enabled_at timestamp with time zone NULL,
    expired_at timestamp with time zone NULL,
    secret varchar(8196) NULL,
    trustworthy integer NOT NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_account_auth_factors PRIMARY KEY (id),
    CONSTRAINT fk_account_auth_factors_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_account_auth_factors_account_id;
CREATE INDEX ix_account_auth_factors_account_id ON account_auth_factors (account_id);

-- ---------------------------------------------------------------------------
-- account_contacts
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS account_contacts CASCADE;
CREATE TABLE account_contacts (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    content varchar(1024) NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    is_primary boolean NOT NULL,
    is_public boolean NOT NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    verified_at timestamp with time zone NULL,
    CONSTRAINT pk_account_contacts PRIMARY KEY (id),
    CONSTRAINT fk_account_contacts_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_account_contacts_account_id;
CREATE INDEX ix_account_contacts_account_id ON account_contacts (account_id);

-- ---------------------------------------------------------------------------
-- account_connections
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS account_connections CASCADE;
CREATE TABLE account_connections (
    id uuid NOT NULL,
    access_token varchar(4096) NULL,
    account_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    is_public boolean NOT NULL,
    last_used_at timestamp with time zone NULL,
    meta jsonb NOT NULL,
    provided_identifier varchar(8192) NOT NULL,
    provider varchar(4096) NOT NULL,
    refresh_token varchar(4096) NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_account_connections PRIMARY KEY (id),
    CONSTRAINT fk_account_connections_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_account_connections_account_id;
CREATE INDEX ix_account_connections_account_id ON account_connections (account_id);

-- ---------------------------------------------------------------------------
-- account_passkeys
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS account_passkeys CASCADE;
CREATE TABLE account_passkeys (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    credential varchar(8196) NOT NULL,
    credential_id varchar(4096) NOT NULL,
    deleted_at timestamp with time zone NULL,
    label varchar(256) NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_account_passkeys PRIMARY KEY (id),
    CONSTRAINT fk_account_passkeys_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_account_passkeys_account_id;
CREATE INDEX ix_account_passkeys_account_id ON account_passkeys (account_id);

DROP INDEX IF EXISTS ix_account_passkeys_credential_id;
CREATE UNIQUE INDEX ix_account_passkeys_credential_id ON account_passkeys (credential_id) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- punishments
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS punishments CASCADE;
CREATE TABLE punishments (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    blocked_permissions jsonb NULL,
    created_at timestamp with time zone NOT NULL,
    creator_id uuid NULL,
    deleted_at timestamp with time zone NULL,
    expired_at timestamp with time zone NULL,
    reason varchar(8192) NOT NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_punishments PRIMARY KEY (id),
    CONSTRAINT fk_punishments_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE,
    CONSTRAINT fk_punishments_accounts_creator_id FOREIGN KEY (creator_id) REFERENCES accounts (id)
);

DROP INDEX IF EXISTS ix_punishments_account_id;
CREATE INDEX ix_punishments_account_id ON punishments (account_id);

DROP INDEX IF EXISTS ix_punishments_creator_id;
CREATE INDEX ix_punishments_creator_id ON punishments (creator_id);

-- ---------------------------------------------------------------------------
-- authorized_apps
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS authorized_apps CASCADE;
CREATE TABLE authorized_apps (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    app_id uuid NOT NULL,
    app_name varchar(1024) NULL,
    app_slug varchar(1024) NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    last_authorized_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone NULL,
    scopes jsonb NOT NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_authorized_apps PRIMARY KEY (id),
    CONSTRAINT fk_authorized_apps_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_authorized_apps_account_id_app_id_type;
CREATE UNIQUE INDEX ix_authorized_apps_account_id_app_id_type ON authorized_apps (account_id, app_id, type) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- action_logs
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS action_logs CASCADE;
CREATE TABLE action_logs (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    action varchar(4096) NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    ip_address varchar(128) NULL,
    location jsonb NULL,
    meta jsonb NOT NULL,
    session_id uuid NULL,
    updated_at timestamp with time zone NOT NULL,
    user_agent varchar(512) NULL,
    CONSTRAINT pk_action_logs PRIMARY KEY (id),
    CONSTRAINT fk_action_logs_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_action_logs_account_id;
CREATE INDEX ix_action_logs_account_id ON action_logs (account_id);

-- ---------------------------------------------------------------------------
-- auth_challenges
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS auth_challenges CASCADE;
CREATE TABLE auth_challenges (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    approved_at timestamp with time zone NULL,
    approved_by_session_id uuid NULL,
    audiences jsonb NOT NULL,
    blacklist_factors jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    declined_at timestamp with time zone NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(512) NOT NULL,
    device_name varchar(1024) NULL,
    expired_at timestamp with time zone NULL,
    failed_attempts integer NOT NULL,
    ip_address varchar(128) NULL,
    location jsonb NULL,
    nonce varchar(1024) NULL,
    platform integer NOT NULL,
    scopes jsonb NOT NULL,
    step_remain integer NOT NULL,
    step_total integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    user_agent varchar(512) NULL,
    CONSTRAINT pk_auth_challenges PRIMARY KEY (id),
    CONSTRAINT fk_auth_challenges_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_auth_challenges_account_id;
CREATE INDEX ix_auth_challenges_account_id ON auth_challenges (account_id);

-- ---------------------------------------------------------------------------
-- e2ee_devices
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS e2ee_devices CASCADE;
CREATE TABLE e2ee_devices (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(512) NOT NULL,
    device_label varchar(1024) NULL,
    is_revoked boolean NOT NULL,
    last_bundle_at timestamp with time zone NULL,
    revoked_at timestamp with time zone NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_e2ee_devices PRIMARY KEY (id),
    CONSTRAINT fk_e2ee_devices_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_e2ee_devices_account_id_device_id_deleted_at;
CREATE UNIQUE INDEX ix_e2ee_devices_account_id_device_id_deleted_at ON e2ee_devices (account_id, device_id, deleted_at);

-- ---------------------------------------------------------------------------
-- e2ee_key_bundles
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS e2ee_key_bundles CASCADE;
CREATE TABLE e2ee_key_bundles (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    algorithm varchar(32) NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(512) NOT NULL,
    identity_key bytea NOT NULL,
    meta jsonb NULL,
    signed_pre_key bytea NOT NULL,
    signed_pre_key_expires_at timestamp with time zone NULL,
    signed_pre_key_id integer NULL,
    signed_pre_key_signature bytea NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_e2ee_key_bundles PRIMARY KEY (id),
    CONSTRAINT fk_e2ee_key_bundles_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_e2ee_key_bundles_account_id_device_id_deleted_at;
CREATE UNIQUE INDEX ix_e2ee_key_bundles_account_id_device_id_deleted_at ON e2ee_key_bundles (account_id, device_id, deleted_at);

-- ---------------------------------------------------------------------------
-- e2ee_one_time_pre_keys
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS e2ee_one_time_pre_keys CASCADE;
CREATE TABLE e2ee_one_time_pre_keys (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    claimed_at timestamp with time zone NULL,
    claimed_by_account_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(512) NOT NULL,
    is_claimed boolean NOT NULL,
    key_bundle_id uuid NOT NULL,
    key_id integer NOT NULL,
    public_key bytea NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_e2ee_one_time_pre_keys PRIMARY KEY (id),
    CONSTRAINT fk_e2ee_one_time_pre_keys_e2ee_key_bundles_key_bundle_id FOREIGN KEY (key_bundle_id) REFERENCES e2ee_key_bundles (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_e2ee_one_time_pre_keys_key_bundle_id_is_claimed;
CREATE INDEX ix_e2ee_one_time_pre_keys_key_bundle_id_is_claimed ON e2ee_one_time_pre_keys (key_bundle_id, is_claimed);

DROP INDEX IF EXISTS ix_e2ee_one_time_pre_keys_account_id_device_id_is_claimed;
CREATE INDEX ix_e2ee_one_time_pre_keys_account_id_device_id_is_claimed ON e2ee_one_time_pre_keys (account_id, device_id, is_claimed);

DROP INDEX IF EXISTS ix_e2ee_one_time_pre_keys_key_bundle_id_key_id_deleted_at;
CREATE UNIQUE INDEX ix_e2ee_one_time_pre_keys_key_bundle_id_key_id_deleted_at ON e2ee_one_time_pre_keys (key_bundle_id, key_id, deleted_at);

-- ---------------------------------------------------------------------------
-- e2ee_sessions
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS e2ee_sessions CASCADE;
CREATE TABLE e2ee_sessions (
    id uuid NOT NULL,
    account_a_id uuid NOT NULL,
    account_b_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    hint varchar(128) NULL,
    initiated_by_id uuid NOT NULL,
    last_message_at timestamp with time zone NULL,
    meta jsonb NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_e2ee_sessions PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_e2ee_sessions_account_a_id_account_b_id_deleted_at;
CREATE UNIQUE INDEX ix_e2ee_sessions_account_a_id_account_b_id_deleted_at ON e2ee_sessions (account_a_id, account_b_id, deleted_at);

-- ---------------------------------------------------------------------------
-- e2ee_envelopes
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS e2ee_envelopes CASCADE;
CREATE TABLE e2ee_envelopes (
    id uuid NOT NULL,
    acked_at timestamp with time zone NULL,
    ciphertext bytea NOT NULL,
    client_message_id varchar(128) NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    delivered_at timestamp with time zone NULL,
    delivery_status integer NOT NULL,
    expires_at timestamp with time zone NULL,
    group_id varchar(256) NULL,
    header bytea NULL,
    legacy_account_scoped boolean NOT NULL,
    meta jsonb NULL,
    recipient_account_id uuid NOT NULL,
    recipient_device_id varchar(512) NULL,
    recipient_id uuid NOT NULL,
    sender_device_id varchar(512) NULL,
    sender_id uuid NOT NULL,
    sequence bigint NOT NULL,
    session_id uuid NULL,
    signature bytea NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_e2ee_envelopes PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_e2ee_envelopes_session_id;
CREATE INDEX ix_e2ee_envelopes_session_id ON e2ee_envelopes (session_id);

DROP INDEX IF EXISTS ix_e2ee_envelopes_recipient_account_id_recipient_device_id_del;
CREATE INDEX ix_e2ee_envelopes_recipient_account_id_recipient_device_id_del ON e2ee_envelopes (recipient_account_id, recipient_device_id, delivery_status, sequence);

DROP INDEX IF EXISTS ix_e2ee_envelopes_recipient_account_id_recipient_device_id_sen;
CREATE UNIQUE INDEX ix_e2ee_envelopes_recipient_account_id_recipient_device_id_sen ON e2ee_envelopes (recipient_account_id, recipient_device_id, sender_id, sender_device_id, client_message_id, deleted_at) WHERE client_message_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- mls_key_packages
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS mls_key_packages CASCADE;
CREATE TABLE mls_key_packages (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    ciphersuite varchar(128) NOT NULL,
    consumed_at timestamp with time zone NULL,
    consumed_by_account_id uuid NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(512) NOT NULL,
    device_label varchar(1024) NULL,
    is_consumed boolean NOT NULL,
    key_package bytea NOT NULL,
    meta jsonb NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_mls_key_packages PRIMARY KEY (id),
    CONSTRAINT fk_mls_key_packages_accounts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
);

DROP INDEX IF EXISTS ix_mls_key_packages_account_id_device_id_is_consumed;
CREATE INDEX ix_mls_key_packages_account_id_device_id_is_consumed ON mls_key_packages (account_id, device_id, is_consumed);

-- ---------------------------------------------------------------------------
-- mls_group_states
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS mls_group_states CASCADE;
CREATE TABLE mls_group_states (
    id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    epoch bigint NOT NULL,
    group_info bytea NOT NULL,
    last_commit_at timestamp with time zone NULL,
    meta jsonb NULL,
    mls_group_id varchar(256) NOT NULL,
    ratchet_tree bytea NOT NULL,
    state_version bigint NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_mls_group_states PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_mls_group_states_mls_group_id_epoch;
CREATE INDEX ix_mls_group_states_mls_group_id_epoch ON mls_group_states (mls_group_id, epoch);

-- ---------------------------------------------------------------------------
-- mls_device_memberships
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS mls_device_memberships CASCADE;
CREATE TABLE mls_device_memberships (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id varchar(512) NOT NULL,
    joined_epoch bigint NOT NULL,
    last_reshare_completed_at timestamp with time zone NULL,
    last_reshare_required_at timestamp with time zone NULL,
    last_seen_epoch bigint NULL,
    mls_group_id varchar(256) NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_mls_device_memberships PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_mls_device_memberships_mls_group_id_last_seen_epoch;
CREATE INDEX ix_mls_device_memberships_mls_group_id_last_seen_epoch ON mls_device_memberships (mls_group_id, last_seen_epoch);

DROP INDEX IF EXISTS ix_mls_device_memberships_mls_group_id_account_id_device_id_de;
CREATE UNIQUE INDEX ix_mls_device_memberships_mls_group_id_account_id_device_id_de ON mls_device_memberships (mls_group_id, account_id, device_id, deleted_at);

-- ---------------------------------------------------------------------------
-- permission_group_members
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS permission_group_members CASCADE;
CREATE TABLE permission_group_members (
    group_id uuid NOT NULL,
    actor varchar(1024) NOT NULL,
    affected_at timestamp with time zone NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    expired_at timestamp with time zone NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_permission_group_members PRIMARY KEY (group_id, actor),
    CONSTRAINT fk_permission_group_members_permission_groups_group_id FOREIGN KEY (group_id) REFERENCES permission_groups (id) ON DELETE CASCADE
);

-- ---------------------------------------------------------------------------
-- permission_nodes
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS permission_nodes CASCADE;
CREATE TABLE permission_nodes (
    id uuid NOT NULL,
    actor varchar(1024) NOT NULL,
    affected_at timestamp with time zone NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    expired_at timestamp with time zone NULL,
    group_id uuid NULL,
    "key" varchar(1024) NOT NULL,
    type integer NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    "value" jsonb NOT NULL,
    CONSTRAINT pk_permission_nodes PRIMARY KEY (id),
    CONSTRAINT fk_permission_nodes_permission_groups_group_id FOREIGN KEY (group_id) REFERENCES permission_groups (id)
);

DROP INDEX IF EXISTS ix_permission_nodes_group_id;
CREATE INDEX ix_permission_nodes_group_id ON permission_nodes (group_id);

DROP INDEX IF EXISTS ix_permission_nodes_key_actor;
CREATE INDEX ix_permission_nodes_key_actor ON permission_nodes ("key", actor);

-- ---------------------------------------------------------------------------
-- account_profiles (from Passport)
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS account_profiles CASCADE;
CREATE TABLE account_profiles (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    active_badge jsonb NULL,
    background jsonb NULL,
    bio varchar(4096) NULL,
    birthday timestamp with time zone NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    experience integer NOT NULL,
    first_name varchar(256) NULL,
    gender varchar(1024) NULL,
    last_name varchar(256) NULL,
    last_seen_at timestamp with time zone NULL,
    links jsonb NULL,
    location varchar(1024) NULL,
    middle_name varchar(256) NULL,
    picture jsonb NULL,
    pronouns varchar(1024) NULL,
    social_credits double precision NOT NULL,
    time_zone varchar(1024) NULL,
    updated_at timestamp with time zone NOT NULL,
    username_color jsonb NULL,
    verification jsonb NULL,
    CONSTRAINT pk_account_profiles PRIMARY KEY (id)
);

DROP INDEX IF EXISTS ix_account_profiles_account_id;
CREATE UNIQUE INDEX ix_account_profiles_account_id ON account_profiles (account_id);

DROP INDEX IF EXISTS ix_account_profiles_last_seen_at;
CREATE INDEX ix_account_profiles_last_seen_at ON account_profiles (last_seen_at);

-- ---------------------------------------------------------------------------
-- account_relationships (from Passport)
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS account_relationships CASCADE;
CREATE TABLE account_relationships (
    account_id uuid NOT NULL,
    related_id uuid NOT NULL,
    alias varchar(128) NULL,
    created_at timestamp with time zone NOT NULL,
    degrade_to_status smallint NULL,
    deleted_at timestamp with time zone NULL,
    expired_at timestamp with time zone NULL,
    status smallint NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT pk_account_relationships PRIMARY KEY (account_id, related_id)
);
