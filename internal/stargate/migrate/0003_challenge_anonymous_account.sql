-- Anonymous auth challenges (discoverable passkey login, QR login) carry no
-- account at creation time: the account is bound when the flow completes.
-- The C# EF schema allowed NULL account_id; the Go port used the
-- all-zero-UUID sentinel, which violates fk_auth_challenges_accounts_account_id
-- (23503) because no accounts row with that id exists.
--
-- Drop NOT NULL so the sentinel can be stored as NULL. The JSON contract is
-- unchanged: handlers normalize NULL back to the sentinel string, which the
-- Dart SDK casts as String.
ALTER TABLE auth_challenges ALTER COLUMN account_id DROP NOT NULL;
