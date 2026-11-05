-- Forensic trail for auth challenges. origin records which sign-in path
-- opened the challenge (password, passkey, qr, social:<provider>, ...);
-- risk_score / risk_signals snapshot why it demanded the steps it did;
-- trail is an append-only event log (who touched the challenge, from which
-- ip / user agent, with which factor, and with what outcome). A session keeps
-- its challenge_id, so session -> challenge -> trail answers "how did this
-- login happen". All columns are additive and nullable/defaulted.

ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS origin text NULL;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS risk_score double precision NULL;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS risk_signals jsonb NULL;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS trail jsonb NOT NULL DEFAULT '[]'::jsonb;
