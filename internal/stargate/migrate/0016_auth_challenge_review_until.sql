-- Post-login confirmation window. A password-first login publishes its
-- approve/deny prompt after the password verifies, but exchanging the
-- challenge expires it (that is what stops the code from minting more token
-- pairs). review_until keeps the satisfied challenge reviewable — and its
-- session revocable by a denial — without reopening the exchange. Nullable:
-- challenges from every other flow keep the previous lifetime behavior.

ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS review_until timestamp with time zone NULL;
