# Local OAuth clients

Stargate normally loads custom OAuth/OIDC clients from `DysonNetwork.Develop`.
A deployment can run the OAuth provider without Develop by defining clients in
Stargate's local TOML configuration.

## Configuration

Add one `[[oidcProvider.clients]]` entry for each local client:

```toml
[oidcProvider]
issuerUri = "https://nt.solian.app"

[[oidcProvider.clients]]
id = "00000000-0000-0000-0000-000000000001"
slug = "my-client"
name = "My Client"
clientSecret = "replace-with-a-long-random-secret"
status = 2
homeUri = "https://client.example.com"
policyUri = "https://client.example.com/privacy"
termsOfServiceUri = "https://client.example.com/terms"
redirectUris = ["https://client.example.com/oauth/callback"]
allowedScopes = ["openid", "profile", "email"]
isPublicClient = false
```

The configuration is loaded from `CONFIG_PATH`, or from
`config.example.toml` when no path is supplied. In production, copy the example
configuration to a private file and set `CONFIG_PATH` to that file.

## Fields

| Field | Required | Description |
| --- | --- | --- |
| `id` | Yes | Stable client identifier. It is stored in OAuth sessions. |
| `slug` | Yes | Human-readable identifier accepted by authorize and token requests. |
| `name` | No | Display name returned by the authorize and device-code endpoints. |
| `clientSecret` | Private clients only | Secret checked for authorization-code, refresh-token, and device-code grants. |
| `status` | No | Develop-compatible status: `2` is Production, `1` is Staging. Omitted or `0` is treated as Production. |
| `homeUri` | No | Client home page and denied-request fallback URI. |
| `policyUri` | No | Privacy-policy URI exposed by client metadata. |
| `termsOfServiceUri` | No | Terms-of-service URI exposed by client metadata. |
| `redirectUris` | Production clients | Allowed callback URIs. Wildcard matching follows the existing Develop behavior. |
| `allowedScopes` | Yes for requested scopes | Scopes accepted by this client. Scope comparison is case-insensitive. |
| `isPublicClient` | No | Public clients skip client-secret validation and must use PKCE. Defaults to `false`. |

A private client should use a long, randomly generated `clientSecret`. Keep the
configuration file readable only by the Stargate service account.

## Client resolution

Resolution order is:

1. Local configuration, matching `id` or `slug`.
2. `DysonNetwork.Develop`, for clients not defined locally.

Therefore, a local entry takes precedence when it uses the same identifier as a
Develop client. If no local entry matches and Develop is not configured, the
request receives `unauthorized_client` with `Client not found`, which is
expected for an unregistered client.

The token-authentication path also checks local clients when validating an OAuth
session's client binding, so local clients work without any Develop connection.

## Public clients and PKCE

Public clients should not store a secret:

```toml
[[oidcProvider.clients]]
id = "00000000-0000-0000-0000-000000000002"
slug = "desktop-client"
name = "Desktop Client"
status = 2
redirectUris = ["http://127.0.0.1:49152/oauth/callback"]
allowedScopes = ["openid", "profile"]
isPublicClient = true
```

The authorization request for a public client must include a supported
`code_challenge` and `code_challenge_method`. The token request must include the
matching `code_verifier`.

## Migration from Develop

To move a client locally:

1. Copy its stable ID, slug, display metadata, redirect URIs, allowed scopes,
   and public/private-client setting from Develop.
2. Set its secret in `clientSecret` when it is a private client.
3. Deploy the configuration and restart Stargate.
4. Exercise authorize, token, refresh-token, and device-code flows before
   removing the Develop service target.

Do not change the client ID during migration; existing OAuth sessions and
stored authorization-code data refer to that ID.
