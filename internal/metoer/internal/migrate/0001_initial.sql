-- 0001_initial.sql — Metoer schema, ported from the EF Core snapshot
-- (DysonNetwork.Ring/Migrations/AppDatabaseModelSnapshot.cs).
--
-- CRITICAL: this is the LIVE dyson_ring database shared with the C# fleet.
-- Every statement is idempotent (CREATE ... IF NOT EXISTS); there are NO
-- DROP statements. UUIDs are client-generated (the C# side has no DB
-- default either).

CREATE TABLE IF NOT EXISTS notifications (
    id uuid NOT NULL CONSTRAINT pk_notifications PRIMARY KEY,
    account_id uuid NOT NULL,
    app_id character varying(1024) NULL,
    content character varying(4096) NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    meta jsonb NOT NULL,
    priority integer NOT NULL,
    push_type character varying(64) NULL,
    subtitle character varying(2048) NULL,
    title character varying(1024) NULL,
    topic character varying(1024) NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    viewed_at timestamp with time zone NULL
);

CREATE TABLE IF NOT EXISTS push_subscriptions (
    id uuid NOT NULL CONSTRAINT pk_push_subscriptions PRIMARY KEY,
    account_id uuid NOT NULL,
    app_id character varying(1024) NULL,
    count_delivered integer NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    device_id character varying(8192) NOT NULL,
    device_name character varying(8192) NULL,
    device_token character varying(8192) NOT NULL,
    is_activated boolean NOT NULL,
    last_used_at timestamp with time zone NULL,
    provider integer NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

-- Convergence for databases created before the current model (the local dev
-- dyson_ring predates the EF migrations; the C# fleet's MigrateAsync brings
-- its DB to the snapshot shape below). Idempotent: no-ops when the columns
-- already exist. Runs before the filtered indexes below, which reference
-- is_activated.
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS app_id character varying(1024) NULL;

ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS app_id character varying(1024) NULL;
ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS is_activated boolean NOT NULL DEFAULT true;
ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS device_name character varying(8192) NULL;
ALTER TABLE push_subscriptions ALTER COLUMN is_activated DROP DEFAULT;

CREATE UNIQUE INDEX IF NOT EXISTS ix_push_subscriptions_account_id_device_id_deleted_at
    ON push_subscriptions (account_id, device_id, deleted_at)
    WHERE deleted_at IS NULL AND is_activated;

CREATE UNIQUE INDEX IF NOT EXISTS ix_push_subscriptions_account_id_device_id_provider_deleted_at
    ON push_subscriptions (account_id, device_id, provider, deleted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS notification_preferences (
    id uuid NOT NULL CONSTRAINT pk_notification_preferences PRIMARY KEY,
    account_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    preference integer NOT NULL,
    topic character varying(1024) NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ix_notification_preferences_account_id_topic_deleted_at
    ON notification_preferences (account_id, topic, deleted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS email_sending_plans (
    id uuid NOT NULL CONSTRAINT pk_email_sending_plans PRIMARY KEY,
    advanced_intervals_count integer NOT NULL,
    broadcast_to_all boolean NOT NULL,
    completed_at timestamp with time zone NULL,
    created_at timestamp with time zone NOT NULL,
    created_by_account_id uuid NOT NULL,
    deleted_at timestamp with time zone NULL,
    html_body character varying(1000000) NOT NULL,
    interval_minutes integer NOT NULL,
    last_advanced_at timestamp with time zone NULL,
    max_emails_per_day integer NULL,
    max_emails_per_interval integer NOT NULL,
    next_interval_at timestamp with time zone NULL,
    paused_at timestamp with time zone NULL,
    planned_start_at timestamp with time zone NOT NULL,
    recipient_count integer NOT NULL,
    sending_plan_key character varying(256) NULL,
    status integer NOT NULL,
    subject character varying(1024) NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ix_email_sending_plans_sending_plan_key_deleted_at
    ON email_sending_plans (sending_plan_key, deleted_at)
    WHERE deleted_at IS NULL AND sending_plan_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS email_sending_plan_recipients (
    id uuid NOT NULL CONSTRAINT pk_email_sending_plan_recipients PRIMARY KEY,
    account_id uuid NOT NULL,
    attempt_count integer NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    last_error character varying(4096) NULL,
    last_interval_number integer NULL,
    last_resolved_email character varying(1024) NULL,
    plan_id uuid NOT NULL,
    processed_at timestamp with time zone NULL,
    recipient_name_snapshot character varying(1024) NULL,
    status integer NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ix_email_sending_plan_recipients_plan_id_account_id_deleted_at
    ON email_sending_plan_recipients (plan_id, account_id, deleted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS email_sending_plan_advances (
    id uuid NOT NULL CONSTRAINT pk_email_sending_plan_advances PRIMARY KEY,
    attempted_count integer NOT NULL,
    completed_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    failed_count integer NOT NULL,
    interval_number integer NOT NULL,
    is_manual boolean NOT NULL,
    pending_count_after integer NOT NULL,
    plan_id uuid NOT NULL,
    sent_count integer NOT NULL,
    skipped_count integer NOT NULL,
    started_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ix_email_sending_plan_advances_plan_id_interval_number_deleted_at
    ON email_sending_plan_advances (plan_id, interval_number, deleted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS email_delivery_records (
    id uuid NOT NULL CONSTRAINT pk_email_delivery_records PRIMARY KEY,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    duration_milliseconds bigint NOT NULL,
    error character varying(4096) NULL,
    outcome integer NOT NULL,
    provider character varying(64) NOT NULL,
    source character varying(64) NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_email_delivery_records_created_at_outcome
    ON email_delivery_records (created_at, outcome);

CREATE TABLE IF NOT EXISTS notification_delivery_records (
    id uuid NOT NULL CONSTRAINT pk_notification_delivery_records PRIMARY KEY,
    app_id character varying(1024) NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    duration_milliseconds bigint NOT NULL,
    error character varying(4096) NULL,
    notification_id uuid NULL,
    outcome integer NOT NULL,
    provider character varying(64) NOT NULL,
    push_type character varying(64) NULL,
    subscription_id uuid NULL,
    topic character varying(1024) NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_notification_delivery_records_created_at_topic_provider_outcome
    ON notification_delivery_records (created_at, topic, provider, outcome);

CREATE INDEX IF NOT EXISTS ix_notification_delivery_records_notification_id_subscription_id_provider_outcome
    ON notification_delivery_records (notification_id, subscription_id, provider, outcome);

CREATE TABLE IF NOT EXISTS notification_send_records (
    id uuid NOT NULL CONSTRAINT pk_notification_send_records PRIMARY KEY,
    app_id character varying(1024) NULL,
    created_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    push_type character varying(64) NULL,
    source character varying(64) NOT NULL,
    topic character varying(1024) NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_notification_send_records_created_at_topic
    ON notification_send_records (created_at, topic);
