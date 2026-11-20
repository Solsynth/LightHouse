# File ACLs and the folder tree

Files and folders share one `cloud_files` table. Two changes make access checks
and tree operations flat and predictable: a materialized `path` column instead
of recursive ancestor CTEs, and a bitmask ACL (`file_acl_entries`) instead of
the legacy `file_permissions` strings.

## Tree columns

| Column | Meaning |
| --- | --- |
| `parent_id` | Adjacency link for folder children. `NULL` at a namespace root. |
| `path` | Slash-delimited ancestor **IDs**, excluding self: `/` at a root, `/<parentID>/` one level down. |
| `depth` | `0` at a root, `parent.depth + 1` below it. |
| `inherit_acl` | When `false`, ACL evaluation stops at this node (see below). |
| `has_acl` | Fast-path flag: the node has at least one direct `file_acl_entries` row. |
| `derived_from_id` | Thumbnails and compressions point at their source here, not through `parent_id`, so they never appear as folder children. |

Derivatives carry `parent_id = NULL`, `path = '/'`, `depth = 0`,
`indexed = false`. Their access check delegates to the source file.

`INSERT` computes `path`/`depth` from the parent in `CloudFile.BeforeCreate`.
Every other tree write (`MoveBatch`, folder creation, migration) sets them
explicitly. `Service.MoveBatch` rewrites a moved folder's whole subtree with one
`UPDATE ... SET path = REPLACE(path, ?, ?), depth = depth + ? WHERE path LIKE ?`
and rejects cycles in memory by checking whether the destination path already
contains the moved id.

## Permission mask

```
Read    = 1   // metadata, download, list children
Write   = 2   // upload/create inside, edit, overwrite, rename
Delete  = 4   // recycle, restore, purge, move out
Share   = 8   // grant/revoke up to own permissions
Manage  = 16  // edit ACLs, toggle inherit_acl
```

Role presets: `viewer` = read, `editor` = read+write, `contributor` =
read+write+delete, `manager` = all bits. `ParsePermissionMask` also accepts the
legacy strings `read`, `write`, `delete`, `share`, `manage`.

## ACL entries

`file_acl_entries` is hard-deleted (no soft-delete column) with a unique key on
`(file_id, subject_type, subject_id, effect)`.

| Field | Notes |
| --- | --- |
| `subject_type` | `account`, `workspace`, `workspace_role`, `workspace_group`, `authenticated`, `public`, `link`, `scope`. |
| `subject_id` | Account UUID, workspace UUID, `"<workspaceID>:<role>"` for roles, group key for groups, share-link id for `link`, empty for `public`/`authenticated`. |
| `effect` | `allow` or `deny`. Deny wins. |
| `permissions` | Bitmask above. |
| `inheritable` | Ancestor entries apply to descendants only when true. |
| `expires_at` | `NULL` = permanent; expired rows are ignored at evaluation time. |

`workspace_group` entries are accepted only on workspace files. Individual
(personal) files keep groups out of their ACL.

Every write to a file's entries updates `cloud_files.has_acl` in the same
transaction, and callers' in-memory rows are refreshed so a subsequent
evaluation does not trust a stale `false`.

## Evaluation

`FileService.EvaluateFiles` resolves a whole page at once:

1. Derivatives are mapped to their source row in one query.
2. Ancestor ids come from `path` — no DB round trip.
3. One query loads `id, inherit_acl, has_acl` for those ancestors.
4. For each file, the chain is self → nearest parent → … and stops after the
   first node with `inherit_acl = false` (that node still applies).
5. One query loads the active entries of every node in the chain that has
   `has_acl = true`. Ancestor entries count only when `inheritable`.
6. `effective = allowMask &^ denyMask`.

Precedence:

1. Superusers always get every bit.
2. A personal file's owner always gets every bit.
3. Inheritance boundary as described above.
4. Explicit deny wins over allow.
5. Workspace files: if the chain was not broken, an active member gets their
   role baseline (`owner`/`admin` → manager, `member` → editor, `viewer` →
   viewer). `inherit_acl = false` on any ancestor removes that baseline from the
   subtree, so only explicit entries remain.
6. Shared pools: a file in an account-less, non-hidden pool is publicly
   readable unless the chain contains a public-read deny. This preserves the
   social/chat link behavior. A root folder created in such a pool gets an
   inheritable public-read deny, which keeps whole folder trees private until
   someone removes that entry.
7. Anything else is denied.

`PrincipalSet` caches the caller's workspace role and group lookups for the
life of one request, so listing 50 files never repeats them.

## HTTP API

| Method | Path | Needs |
| --- | --- | --- |
| `GET` | `/api/files/:id/acl` | Manage |
| `PUT` | `/api/files/:id/acl` | Manage |
| `POST` | `/api/files/:id/acl/entries` | Manage |
| `DELETE` | `/api/files/:id/acl/entries/:entryId` | Manage |
| `GET` | `/api/files/shared-with-me` | signed in |

`GET` returns `inherit_acl`, `direct` entries, `inherited` entries with
`source_file_id`/`source_file_name`, and the caller's `effective`
`PermissionStatus`. `PUT` replaces the direct entries atomically and accepts
`{ "inherit_acl": bool, "entries": [...] }`; each entry may carry `role` instead
of `permissions`.

`CloudFile.permission_status` now reflects the caller
(`readable`, `writable`, `deletable`, `shareable`, `manageable`,
`visibility`, `inherit_acl`, `effective_mask`, `inherited_from`).

Enforcement uses the exact bit at each site: read for metadata/download/listing,
write for content writes, uploads into a folder and folder creation, delete for
recycle/restore/purge/move-out, manage for ACL edits. Batch endpoints authorize
every selected row in one evaluation pass, and moves also require write on the
destination folder. WebDAV lists readable children, resolves paths under read,
and requires delete/write for deletes and moves. S3 object reads and deletes go
through the same evaluator.

## Compatibility

- `GET/PUT /api/files/:id/permissions` still exist. They project ACL rows into
  the legacy `FilePermission` shape: a public deny becomes `private`, allow
  entries keep their subject and map to `read`/`write`/`manage`.
- gRPC `SetFilePublic`/`UnsetFilePublic` write a public allow/deny entry.

## Migration

`database.MigrateACLAndTree` runs from the core schema hook on every startup and
is idempotent:

1. Adds `idx_cloud_files_path_pattern` (`text_pattern_ops`, partial) on
   PostgreSQL.
2. Moves `system.thumbnail` / `system.compression*` rows from `parent_id` to
   `derived_from_id`.
3. Normalizes root rows to `path = '/'`, `depth = 0`.
4. If any nested row's path does not end with its own id, or its depth does not
   equal the parent's depth plus one, recomputes all paths and depths from the
   adjacency tree.
5. Converts live `file_permissions` rows per file: `private` (without `public`)
   becomes an inheritable public deny plus `inherit_acl = false`, other subjects
   become allow entries, and `has_acl` is set.

The legacy importer runs the same migration after copying rows, so a freshly
imported tree and its permission rows are usable immediately.

## Checking PostgreSQL

`TestPostgresACLAndTree` validates the `text_pattern_ops` index, the subtree
rewrite and the depth arithmetic against a disposable database:

```
LIGHTHOUSE_TEST_POSTGRES_DSN="host=… user=… password=… dbname=… sslmode=disable" \
  GOWORK=off go test ./service/ -run TestPostgresACLAndTree
```

The test drops and recreates the file tables it uses.

## Account groups

`workspace_group` entries need the caller's workspace group ids. Valve owns
them:

- Tables `workspace_groups` (per workspace, unique live name) and
  `workspace_group_members` (unique live pair, only active workspace members).
- gRPC `DyWorkspaceService.GetAccountWorkspaceGroups(workspace_id, account_id)`
  returns the account's group ids.
- REST management under `/api/workspaces/:slug/groups`: list/create, patch or
  delete a group, add or remove members. Reads need workspace view access;
  mutations need an active admin.

`QuotaService.SetWorkspaceClient` installs the group fetcher alongside the role
resolver, so any deployment with a Valve workspace endpoint resolves both
workspace roles and groups. Group subjects match nobody in a deployment without
that endpoint, while account grants and workspace roles keep working.

The RPC is part of `shared/proto`; the shared module must be tagged
`shared/v0.2.9` (it is v0.2.8 plus this RPC) before standalone module builds and
container images resolve it from the module proxy.
