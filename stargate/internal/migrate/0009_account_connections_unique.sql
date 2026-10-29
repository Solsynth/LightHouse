-- account_connections had no unique constraint and the upsert paths were
-- check-then-insert, so concurrent OIDC callbacks could insert two rows for
-- the same (account_id, provider, provided_identifier).
--
-- 1. Deduplicate live rows: keep the registration row (registered_at set) if
--    any, otherwise the freshest; merge tokens from the freshest row so the
--    survivor keeps working credentials.
-- 2. Add a partial unique index so the race is impossible going forward. The
--    store's ON CONFLICT upserts target this exact index.

-- Merge tokens into the keeper row from the freshest row of the same key.
WITH ranked AS (
    SELECT id, account_id, LOWER(provider) AS prov, provided_identifier AS ident,
           updated_at, access_token, refresh_token,
           ROW_NUMBER() OVER (
               PARTITION BY account_id, LOWER(provider), provided_identifier
               ORDER BY (registered_at IS NOT NULL) DESC, updated_at DESC, created_at DESC
           ) AS rn
    FROM account_connections
    WHERE deleted_at IS NULL
),
fresh AS (
    SELECT DISTINCT ON (account_id, prov, ident)
           account_id, prov, ident, access_token, refresh_token
    FROM ranked
    ORDER BY account_id, prov, ident, updated_at DESC
)
UPDATE account_connections k
SET access_token  = COALESCE(k.access_token, f.access_token),
    refresh_token = COALESCE(k.refresh_token, f.refresh_token)
FROM ranked r
JOIN fresh f ON f.account_id = r.account_id AND f.prov = r.prov AND f.ident = r.ident
WHERE r.rn = 1 AND k.id = r.id
  AND (k.access_token IS NULL OR k.refresh_token IS NULL);

-- Delete the duplicate rows.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY account_id, LOWER(provider), provided_identifier
               ORDER BY (registered_at IS NOT NULL) DESC, updated_at DESC, created_at DESC
           ) AS rn
    FROM account_connections
    WHERE deleted_at IS NULL
)
DELETE FROM account_connections a
USING ranked r
WHERE a.id = r.id AND r.rn > 1;

-- Enforce one live connection per (account_id, provider, provided_identifier).
CREATE UNIQUE INDEX IF NOT EXISTS ux_account_connections_account_provider_identifier
    ON account_connections (account_id, LOWER(provider), provided_identifier)
    WHERE deleted_at IS NULL;
