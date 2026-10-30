# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately**. Do not open a public
issue, pull request or discussion for a security problem.

- **GitHub private vulnerability reporting** (preferred): use the repository's
  **Security** tab → **Report a vulnerability** under the
  [Solsynth](https://github.com/Solsynth) organization. This opens a private
  advisory visible only to you and the maintainers.
- **Email**: [security@solsynth.dev](mailto:security@solsynth.dev). Encrypt
  sensitive details if you can; we will send a PGP key on request.

Include as much of the following as you can:

- the affected service and endpoint (for example `Stargate`,
  `POST /api/auth/challenge`);
- a reproduction (request, response, and the account/permission level used);
- the impact and any data you believe is exposed;
- your preferred credit name, if you want one.

## What to expect

- Acknowledgement of your report within **72 hours**.
- An assessment and severity classification within **7 days**.
- Progress updates at least every **14 days** until the report is resolved.
- Credit in the advisory unless you ask to stay anonymous.

We ask that you give us a reasonable window to ship a fix before publishing.
Testing against accounts and data you own (or empty/invalid identifiers) is
fine; do not access, modify or exfiltrate other users' data, and do not run
denial-of-service tests against production.

## Scope

In scope: the services in this repository (`Stargate`) and the API surfaces it
exposes through the gateway (`/api/**`, `/.well-known/**`).

Out of scope: findings that require a compromised client device, social
engineering, physical access, or already-public information; third-party
services we only integrate with (payment, email, OIDC providers).

## Machine-readable contact

`GET /.well-known/security.txt` serves the same contact as an
[RFC 9116](https://www.rfc-editor.org/rfc/rfc9116) document.
