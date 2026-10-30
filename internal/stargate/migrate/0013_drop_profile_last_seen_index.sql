-- account_profiles.last_seen_at is never queried: every read is by
-- account_id (the unique index) and every write sets last_seen_at on a known
-- row. The index therefore only cost an index write on the hottest write
-- path in the service — the fleet's last-active touch, which fires on every
-- authenticated request — while prod stats showed 2 scans against 1.15M row
-- updates (and the index itself growing to ~1 MB, over a third of the table).
DROP INDEX IF EXISTS ix_account_profiles_last_seen_at;
