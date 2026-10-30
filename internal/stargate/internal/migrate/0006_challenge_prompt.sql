-- Marks when an in-app approval prompt was last requested for a challenge,
-- replacing the "code already sent" Redis guard with a DB signal.
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS prompt_requested_at timestamp with time zone NULL;
