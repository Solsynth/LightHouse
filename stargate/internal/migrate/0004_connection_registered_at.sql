-- account_connections.registered_at marks the connection that CREATED the
-- account (OIDC/social registration), for moderation. It stays NULL for
-- connections linked to an already-existing account. Registration
-- connections cannot be removed by the account owner.

ALTER TABLE account_connections ADD COLUMN IF NOT EXISTS registered_at timestamp with time zone NULL;

-- Backfill: a connection created within 60s of its account is the
-- registration connection (social signups create the account and the
-- connection in the same request; later linkings happen well after account
-- creation, and direct registrations have no connection rows at all).
UPDATE account_connections c
SET registered_at = c.created_at
FROM accounts a
WHERE c.account_id = a.id
  AND c.registered_at IS NULL
  AND c.deleted_at IS NULL
  AND ABS(EXTRACT(EPOCH FROM (c.created_at - a.created_at))) <= 60;
