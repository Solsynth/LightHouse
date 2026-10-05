# OAuth session devices

OAuth and OIDC sessions (`auth_sessions` with `type` `OAuth`/`Oidc`) authorize
third-party apps. Stargate binds each one to a device row in `auth_clients`
through `auth_sessions.client_id`, so the session:

- appears under a device on the security page, and is revoked with it;
- is reused only by the device that authorized it;
- can never be trusted for interactive approval (see [Trust](#trust)).

The binding is resolved in one place per grant, in `internal/httpserver/oidcctl`.

## Where the device comes from

| Grant | Device source (in order) |
| --- | --- |
| Authorization code | The device of the authenticated session that approved the request, then the user-agent/IP fallback. |
| Device authorization | The `device_id` the polling client declares, then the user-agent/IP fallback. |

The fallback is skipped when the request carries neither a user agent nor an IP;
the session is then left device-less (`client_id IS NULL`), which is what every
session looked like before device binding existed.

## The authorization code flow

The authorization code flow is handled specially: it needs no extra request
parameters. `POST /api/auth/open/authorize` is authenticated, and the app runs
on the same device as the browser completing the request, so the authorizing
session's device *is* the app's device. The handler stores
`middleware.CurrentSession(ctx).ClientId` on the authorization code
(`authorizationCodeInfo.DeviceId`) and the token exchange binds the session to
it.

A legacy authorizing session may have no device (`client_id IS NULL`). In that
case the token exchange falls back to the user agent and IP of the token
request.

## The device authorization flow

RFC 8628's device flow is the genuinely cross-device case: the app runs on a
device (a TV, a console) that cannot run a browser, and the user approves the
`user_code` on a separate device. The approving device is therefore *not* the
client device, and must not be used as one (RFC 8628 Section 5.3).

Instead, `POST /api/auth/open/device/code` accepts an optional device identity
that the polling client declares:

| Parameter | Description |
| --- | --- |
| `device_id` | Stable identifier of the device running the app. |
| `device_name` | Display name shown for the device. |
| `platform` | `ClientPlatform` number: `1` Web, `2` iOS, `3` Android, `4` macOS, `5` Windows, `6` Linux. Anything else is treated as unidentified. |

The identity is carried on the device code and resolved at the token request,
where the account is known. A client that declares nothing falls back to the
user agent and IP of the polling request.

Because RFC 8628 treats device clients as public clients (Section 5.6), the
declared identity is untrusted: it labels the session's device and nothing else.
Authorization still comes only from the user approving the `user_code`. Servers
should show the declared name at that step, where the user can notice a client
impersonating a device (Section 5.4).

## User agent and IP fallback

When nothing else identifies the device, Stargate derives one from the request
that carries the grant:

- the device key is `oauth:<client id>:fp:<sha256(user agent + NUL + IP)>`;
- the platform is guessed from the user agent (`android` before `linux`,
  because Android user agents contain both);
- the device name is a label for that platform, for example `iOS device`.

The fingerprint is deterministic, so repeat grants from the same user agent and
IP reuse one device row. A changed IP yields a new row: mobile networks rotate
addresses, so the fallback favours a correct new entry over a wrong merge. Users
can rename the row with the usual device label endpoint.

## Namespacing

A declared or fingerprinted device is stored under a namespaced key:

```
oauth:<client id>:<declared id>
oauth:<client id>:fp:<sha256(user agent + NUL + IP)>
```

without the namespace a client could pass a real hardware `device_id` and attach
its session to an existing device row. Namespacing makes that impossible, so an
OAuth device row is always distinct from any wsgateway device.

`auth_clients.device_id` and `device_name` are `varchar(1024)`. Declared ids are
capped at 512 runes, names at 1024 runes, and a composed key longer than 1024
bytes collapses to a hash of the declared value so two distinct ids never share
a row.

## Session reuse

`FindValidOauthSession` matches on `(account, app, client_id)` with
`client_id IS NOT DISTINCT FROM ?`. Re-authorizing the same app from the same
device extends that session; a different device gets its own. A nil client
matches only device-less sessions, so an authorization from one device never
extends a session that belongs to another. Refresh tokens are bound to their
session, so they are unaffected either way.

## Trust

OAuth and OIDC sessions are never trusted, even though they now carry a device:
`Store.IsTrustedSession` rejects both types. Trust unlocks interactive challenge
approval and QR scanning, and those endpoints are reachable with an OAuth access
token, so a third-party app must not pass the check.

The security page's per-device `trusted` flag is likewise computed from login
sessions only, so an OAuth-only device row cannot show as trusted.

## Standards notes

RFC 8628 defines exactly two request parameters for the device authorization
endpoint — `client_id` and `scope` — and states that "the authorization server
MUST ignore unrecognized request parameters" (Section 3.1). The same rule
applies to the OAuth authorization and token endpoints (RFC 6749 Sections 3.1
and 3.2), and Section 3.4 fixes the device token request to `grant_type`,
`device_code` and `client_id`.

The device identity parameters are therefore a deployment-local extension, not a
standard mechanism:

- a client that omits them works unchanged, and one that sends them still
  interoperates with any compliant server, which ignores them;
- a client must not assume another authorization server honours them;
- adding them to the token request would buy nothing, since `device_code`
  already binds the original request.

RFC 8628 does support the *purpose*: servers "SHOULD display information about
the device so that the user could notice if a software client was attempting to
impersonate a hardware device" (Section 5.4). That is the role the device name
plays here.

## Schema

| Column | Purpose |
| --- | --- |
| `auth_sessions.client_id` | Device the session is bound to; FK to `auth_clients.id`. |
| `auth_clients.device_id` | Device key, namespaced for OAuth devices. |
| `auth_clients.device_name` | Display name; client-declared or a platform label. |
| `auth_clients.platform` | Client platform, client-declared or guessed from the user agent. |

See [local-oauth-clients.md](local-oauth-clients.md) for the client configuration
these parameters are used with.
