-- Add is_temporary and auto_renewable columns to auth_sessions and auth_challenges
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS is_temporary boolean NOT NULL DEFAULT false;
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS auto_renewable boolean NOT NULL DEFAULT true;

ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS is_temporary boolean NOT NULL DEFAULT false;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS auto_renewable boolean NOT NULL DEFAULT true;

-- Cancel nonexpirable login sessions: all login sessions must have an expiry.
-- Existing login sessions with NULL expired_at are expired immediately.
UPDATE auth_sessions
SET expired_at = now(),
    updated_at = now()
WHERE type = 0
  AND expired_at IS NULL;
