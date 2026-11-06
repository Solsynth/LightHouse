-- Action-log summaries filter by account and scan a created_at window.
DROP INDEX IF EXISTS ix_action_logs_account_id_created_at;
CREATE INDEX ix_action_logs_account_id_created_at ON action_logs (account_id, created_at DESC);
