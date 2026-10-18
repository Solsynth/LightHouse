# OAuth apps

`app.solian.id.oauthapps` is the feature module that owns Stargate's OAuth
and OIDC client registry. It stores the registrations the provider
authorizes against, so a deployment does not need `DysonNetwork.Develop` at
all.

The module is **not** the custom-app domain. An OAuth app is only the client
registration: identifier, metadata, redirect URIs, allowed scopes and client
secrets. Develop keeps everything else — developers, dev projects, bot
accounts, mini apps, app products, board widgets — and its custom-app records
remain the source for project-linked apps.

## Ownership

A registration has exactly one owner, enforced by the database
(`ck_oauth_apps_owner`):

| Owner | Column | Use |
|---|---|---|
| A Develop project | `project_id` | The app belongs to a developer team. Managing it requires the developer permission keys. |
| An individual account | `account_id` | The app belongs to one account. Works without the Develop service. |

Creating an app with `project_id` links it to that project; omitting it makes
the calling account the owner. Ownership is set at creation and never changes
afterwards — a `PATCH` with `project_id` is rejected.

## Client resolution

The module publishes the `oauthClients` port. Resolution order, which is the
order the provider documented before the data moved here:

1. Deployment configuration (`[[oidcProvider.clients]]`).
2. This module's table.
3. The `app.solian.develop` integration.

A missing provider is not an error: the provider answers `unauthorized_client`
with `Client not found`, exactly as an unregistered client does.

`app.solian.oauthapps` is a *feature* provider, so it wins over the Develop
integration when both are enabled. Select otherwise with:

```toml
[ports]
oauthClients = "app.solian.develop"
```

## HTTP surface

All routes require a session (`/api/oauth-apps`). Mutations that can leak or
replace a credential require step-up elevation (`RequireSudo`).

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/oauth-apps` | Apps owned by the caller. `?projectId=` lists a project's apps and needs the developer permission. |
| `POST` | `/api/oauth-apps` | Creates an app owned by the caller, or by `project_id`. |
| `GET` | `/api/oauth-apps/{appId}` | `appId` accepts the id or the slug. |
| `PATCH` | `/api/oauth-apps/{appId}` | Partial update. |
| `DELETE` | `/api/oauth-apps/{appId}` | Soft delete, requires elevation. |
| `GET` | `/api/oauth-apps/{appId}/secrets` | Metadata only; the plaintext is never returned. |
| `POST` | `/api/oauth-apps/{appId}/secrets` | Returns the new plaintext once. Requires elevation. |
| `GET` | `/api/oauth-apps/{appId}/secrets/{secretId}` | Metadata only. |
| `DELETE` | `/api/oauth-apps/{appId}/secrets/{secretId}` | Requires elevation. |
| `POST` | `/api/oauth-apps/{appId}/secrets/{secretId}/rotate` | New plaintext, returned once. Requires elevation. |

Request shape:

```json
{
  "slug": "my-client",
  "name": "My Client",
  "description": "optional",
  "status": 2,
  "project_id": "optional-develop-project-id",
  "picture_id": "optional-drive-file-id",
  "background_id": "optional-drive-file-id",
  "oauth_config": {
    "client_uri": "https://client.example",
    "redirect_uris": ["https://client.example/oauth/callback"],
    "allowed_scopes": ["openid", "profile", "email"],
    "allowed_grant_types": ["authorization_code", "refresh_token"],
    "require_pkce": true,
    "allow_offline_access": false,
    "is_public_client": false
  },
  "links": {
    "home_page": "https://client.example",
    "privacy_policy": "https://client.example/privacy",
    "terms_of_service": "https://client.example/terms"
  }
}
```

`status` follows Develop's `CustomAppStatus`: `0` developing, `1` staging,
`2` production, `3` suspended. Only a production client enforces its redirect
URI list, matching the provider's authorize-time behaviour.

`picture_id` and `background_id` name Drive files and are the only fields a
request resolves rather than stores verbatim: the server asks the `files` port
for the file and stores the resulting `SnCloudFileReferenceObject`, so a read
carries the url and dimensions the consent screen needs. Omitting a field
leaves the stored art alone; sending `""` clears it. A deployment with no Files
provider refuses the field with `OAUTH_APP_ART_UNAVAILABLE` rather than
dropping it silently. Reads include `picture`/`background` when set, and the
provider serves them on the consent (`GET /api/auth/open/authorize`) and device
status (`GET /api/auth/open/device/code/{userCode}`) responses.

Secrets accept `{"description": "...", "expires_in": "720h", "type": "oidc"}`.
`type` defaults to `oidc`; `api_key` marks a non-OIDC key. `expires_in`
accepts a Go duration or a number of seconds.

## Cache

The provider caches resolved clients in Redis for five minutes
(`auth:oidc-client:id:*`, `auth:oidc-client:slug:*`, prefixed `dyson:`).
Creating, updating, rotating or deleting an app drops those keys, including
the previous slug after a rename, so edits take effect immediately.

The cache is only read while a provider is mounted. Disabling this module (or
the Develop integration) therefore stops serving its clients at once rather
than after the TTL.

## Schema

The module owns two tables, migrated only while it is enabled:

| Table | Contents |
|---|---|
| `oauth_apps` | Client registration plus its `oauth_config` and `links` jsonb, and the `picture`/`background` file references. |
| `oauth_app_secrets` | Client secrets and API keys (`is_oidc` distinguishes them). |

Secrets are stored in plaintext, as Develop stores them: verification compares
the stored value. A private client secret should be long and random.

## Migrating from Develop

`scripts/import-oauth-apps.sh` copies Develop's `custom_apps` and
`custom_app_secrets` into this module's tables. It keeps ids, so OAuth
sessions issued before the move keep resolving, and it is idempotent.

```sh
PGDRY=1 ./scripts/import-oauth-apps.sh     # dry run first
./scripts/import-oauth-apps.sh
```

Only the OAuth-relevant subset moves. Board widgets, payment wallets, mini
apps, products, bot accounts, developer records and dev projects stay in
Develop; an app that also serves a mini app or a wallet keeps those there and
gains an OAuth registration here.

Rows the importer declines to guess at are counted and skipped rather than
written:

| Situation | Why |
|---|---|
| The target already holds a *different* app with the same slug | the provider resolves clients by slug with a first-match lookup, so a duplicate would make authorization ambiguous |
| The app has no `project_id` | Develop makes it non-null and the target's `ck_oauth_apps_owner` demands exactly one owner, so an ownerless app cannot be represented |
| A secret whose app was skipped | it could never be resolved |

Re-running is safe: `module_import_state` records the counts, and every
insert is `ON CONFLICT DO NOTHING` on the id, so an existing row is never
overwritten. Soft-deleted apps and secrets keep their `deleted_at` and stay
unresolvable. The source database is never modified.

An app whose source `oauth_config` is null imports with an empty config:
Develop's provider reads through a null config and sees no scopes, so the
import does not grant authorization the client never had. Inside a present
config, an absent key takes the same default Develop itself uses.

A source app's `picture` and `background` jsonb copy through unchanged, so a
migrated registration keeps the art its consent screen already showed. Both are
optional in Develop and stay absent here when the source has none.

Disabling `app.solian.id.oauthapps` unmounts `/api/oauth-apps` and drops this
module's provider from the port. Existing tables and rows are never dropped.
With Develop unconfigured, the provider then answers `Client not found`; with
Develop configured, its custom apps keep working.

## Console surface

`/developers` adds an **OAuth Apps** panel above the authorized-app list: every
registration this account owns, each opening at `/developers/apps/{appId}`.
Creating one asks for a name, a slug, and a description and starts it developing
with `openid`, `profile`, `email` and the `authorization_code`/`refresh_token`
grants, so the client is usable before any further setup.

The app page edits identity, OAuth configuration, links, and the client
secrets. Creating, rotating, or deleting a secret — and deleting the app — are
elevation-gated, and the console raises its own elevation sheet. A secret's
plaintext is displayed exactly once, above the metadata-only list. Art is
entered as Drive file ids and resolved by the server, so the page only has to
say that a deployment without Drive cannot store art at all.
