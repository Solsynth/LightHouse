-- Login sessions used to be stamped with the full-grant wildcard scope "*",
-- which PermissionScopeGate.HasFullScope treated as a bypass of the scope gate,
-- the superuser check and the permission service: every [AskPermission]
-- endpoint answered 200 for any authenticated account. Ordinary login sessions
-- only ever hold the scopes the client requested, so strip the wildcard from
-- the rows issued before the stamping was removed. OAuth/OIDC sessions may
-- legitimately carry "*" and are left alone (the gate itself is session-type
-- guarded, so a wildcard in a login session bypasses nothing anyway).

UPDATE auth_sessions
SET scopes     = scopes - '*',
    updated_at = now()
WHERE type = 0
  AND scopes @> '["*"]'::jsonb;
