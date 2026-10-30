-- auth_sessions.refreshed_at records when a refresh grant last rotated this
-- session's epoch. It anchors the rotation grace window: the immediately
-- previous refresh token stays acceptable for a short period after the
-- rotation, so a rotation whose response the caller never received (dropped
-- response, timed-out request, concurrent duplicate) can be retried instead of
-- being rejected as revoked and forcing a re-login. NULL until the first
-- rotation.

ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS refreshed_at timestamp with time zone NULL;
