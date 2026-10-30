-- Elevation ("sudo") challenges reuse auth_challenges. purpose discriminates
-- them from ordinary login challenges; session_id binds the elevation to the
-- session that must receive it (elevation never mints a token); sudo_until
-- records the instant the granted elevation expires so the client can cache
-- the window; extra_factor_type marks the synthetic emailed fallback step.
-- All columns are nullable: ordinary login challenges leave them NULL.

ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS purpose text NULL;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS session_id uuid NULL;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS sudo_until timestamp with time zone NULL;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS extra_factor_type integer NULL;
