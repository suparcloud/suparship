# Single Sign-On (OIDC)

suparShip authenticates users in one of two ways:

- **Local admin credential** — a single break-glass account (username + bcrypt
  password) stored in a Kubernetes Secret. Always available; see
  [install.md](install.md).
- **OIDC single sign-on** — your organisation's identity provider (Google
  Workspace, Okta, Microsoft Entra, Keycloak, Dex, …). Developers log in with
  their IdP account; access is granted by **team membership** or **IdP group**.

SSO is **additive**: enabling it does not disable the local admin, which remains
as break-glass if the IdP is misconfigured or unreachable.

## How authorization works

A logged-in identity is `(username, groups)`:

- **username** comes from the configured `usernameClaim` (default `email`).
- **groups** come from the configured `groupsClaim` (default `groups`).

Role bindings (Settings → Teams → *Role Bindings*) grant a role on a project to
**either** a local Team (matched by the username's team membership) **or** an
IdP **group** (matched against the group claim). Project `*` matches all
projects. Roles, highest to lowest: `org_admin`, `project_admin`, `developer`,
`viewer`.

So there are two ways to authorize an SSO user:

1. **By IdP group** — bind a group to a role. Best when your IdP emits a groups
   claim (most do; **Google Workspace does not by default** — see below).
2. **By team** — add the user's username (their `email`) to a Team, and bind the
   Team to a role. Works with any IdP, no group claim required.

## Configure (UI)

Settings → **Auth**:

| Field | Notes |
|---|---|
| Issuer URL | OIDC issuer; discovery runs at `<issuer>/.well-known/openid-configuration` |
| Client ID | the OAuth2 client registered with your IdP |
| Client Secret | write-only; stored in a Kubernetes Secret, never in config |
| Redirect URL | `https://<your-suparship-host>/api/v1/auth/oidc/callback` |
| Scopes | default `openid profile email groups` |
| Username claim | default `email` |
| Groups claim | default `groups` |

The **Redirect URL must exactly match** the authorized redirect URI registered
with your IdP. After saving, the login page shows a **Sign in with SSO** button.

The same config can be captured for IaC: `GET /api/v1/org/export?format=yaml`
emits an `auth.oidc` block (secret **reference** only, never the value), and
`charts/suparship/values.yaml` documents the schema.

## Google Workspace

1. **Google Cloud console → APIs & Services → Credentials → Create OAuth client
   ID → Web application.**
   - Authorized redirect URI: `https://<your-suparship-host>/api/v1/auth/oidc/callback`
   - Note the **Client ID** and **Client secret**.
2. Configure the OAuth consent screen (Internal user type keeps it to your
   Workspace).
3. In suparShip → Settings → Auth:
   - Issuer URL: `https://accounts.google.com`
   - Client ID / Client Secret: from step 1
   - Redirect URL: as registered above
   - Leave scopes/claims at defaults.
4. **Grant access.** Google's ID token includes `email` but **not a `groups`
   claim** out of the box, so:
   - **Recommended for day 1:** add developer emails to a Team (Settings →
     Teams) and bind the Team to a role. This works immediately.
   - **Group-based access** requires surfacing Workspace groups as a token
     claim — e.g. via the Google Cloud Identity / Directory API or an OIDC proxy
     (Dex with the Google connector + `groups` scope). Once your tokens carry a
     groups claim, set the **Groups claim** accordingly and bind groups to roles.

Other IdPs (Okta, Entra, Keycloak, Dex) typically emit a `groups` claim
directly — bind groups to roles and you're done.

## Break-glass

The local admin always works at `POST /api/v1/auth/login` (the password form on
the login page). If SSO breaks, sign in as the admin and fix the config under
Settings → Auth. The admin is authorized via the `admins` team created by
`suparship admin bootstrap` — SSO changes never affect it.

## Troubleshooting

The callback redirects to `/login?error=<code>` on failure:

| Code | Meaning / fix |
|---|---|
| `sso_unavailable` | OIDC not enabled, or org config unreadable |
| `sso_init` | discovery or client-secret load failed — check Issuer URL and that the client-secret Secret exists |
| `sso_state` | state cookie missing/expired — retry the login (don't bookmark the IdP URL) |
| `sso_denied` | the IdP returned an error (consent denied, app not authorized) |
| `sso_exchange` | code→token exchange failed — check the Client Secret and Redirect URL match the IdP registration |
| `sso_no_id_token` | the IdP returned no ID token — ensure the `openid` scope is requested |
| `sso_verify` | ID-token signature/issuer/audience verification failed — Client ID or Issuer mismatch |
| `sso_nonce` | nonce mismatch — retry; if persistent, suspect a caching proxy on the callback |
| `sso_claims` / `sso_session` | could not read claims / create the session — check server logs |

Server logs (`oidc:` prefix) carry the underlying error for `sso_init`,
`sso_exchange`, and `sso_verify`.
