-- Former account names for redirect fallback after paid renames.
DROP TABLE IF EXISTS account_name_history CASCADE;
CREATE TABLE account_name_history (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    name varchar(256) NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    CONSTRAINT pk_account_name_history PRIMARY KEY (id)
);
DROP INDEX IF EXISTS ix_account_name_history_name_deleted_at;
CREATE UNIQUE INDEX ix_account_name_history_name_deleted_at ON account_name_history (name, deleted_at);
DROP INDEX IF EXISTS ix_account_name_history_account_id;
CREATE INDEX ix_account_name_history_account_id ON account_name_history (account_id);
