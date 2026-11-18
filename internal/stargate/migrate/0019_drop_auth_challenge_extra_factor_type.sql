-- Sudo elevation no longer arms a synthetic emailed fallback step: an account
-- with nothing presentable (no factor at or above SudoMinTrustworthy) is
-- treated as lifted instead of being asked for a code that could never arrive.
-- The column is dropped; existing rows always left it NULL or stale.

ALTER TABLE auth_challenges DROP COLUMN IF EXISTS extra_factor_type;
