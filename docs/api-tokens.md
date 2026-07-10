# Project API tokens

API tokens authenticate non-interactive callers — CI pipelines, scripts, bots —
against the suparship API. A token is scoped to a **single project** and carries
a **fixed RBAC role**; it authenticates via the standard `Authorization: Bearer`
header, independently of the human who created it.

```
Authorization: Bearer supat_<id><secret>
```

## Model

- **Scope** — one project. A token for project `voiceai` can never act on another
  project, regardless of its role.
- **Role** — one of `viewer`, `developer`, `project_admin`, chosen at creation
  (default `developer`). The token authorizes exactly as that role on its project.
  It is **never** an org admin, even if the person who minted it is — org-level
  endpoints always reject token auth.
- **Lifetime** — tokens never expire by default. Pass `expiresInDays` to set a
  deadline. Revoke at any time by `id`.
- **Storage** — only a SHA-256 hash of the token's secret is persisted (in the
  `suparship-api-tokens` Secret in `suparship-system`). The plaintext is shown
  **once**, in the create response, and is unrecoverable afterwards.

## Managing tokens

Minting, listing, and revoking all require **project_admin** on the project.

### Create

```bash
curl -fsS -X POST "$SUPARSHIP_API/projects/$PROJECT/tokens" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \      # or a session cookie from the UI
  -H 'Content-Type: application/json' \
  -d '{"name":"github-actions","role":"developer","expiresInDays":90}'
```

`role` and `expiresInDays` are optional (defaults: `developer`, never expires).
The response carries the one-time plaintext `token` plus metadata:

```json
{
  "token": "supat_1a2b3c…",
  "id": "1a2b3c4d5e6f7a8b",
  "name": "github-actions",
  "project": "voiceai",
  "role": "developer",
  "createdBy": "alice",
  "createdAt": "2026-06-23T10:00:00Z",
  "expiresAt": "2026-09-21T10:00:00Z"
}
```

Store the `token` value as a secret in your CI provider. Keep the `id` to revoke
it later.

### List

```bash
curl -fsS "$SUPARSHIP_API/projects/$PROJECT/tokens" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

Returns metadata only — never the secret.

### Revoke

```bash
curl -fsS -X DELETE "$SUPARSHIP_API/projects/$PROJECT/tokens/$TOKEN_ID" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

## Using a token

Any API call that accepts a session cookie also accepts a bearer token, subject
to the token's project and role. For example, the preview CI flow in
[`examples/preview-from-pr.yml`](../examples/preview-from-pr.yml) uses a
`developer`-role token to create and delete previews:

```bash
curl -fsS -X POST "$SUPARSHIP_API/projects/$PROJECT/apps/$APP/previews" \
  -H "Authorization: Bearer $SUPARSHIP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"pr-42"}'
```

A token presented to an endpoint above its role (e.g. a `developer` token on a
`project_admin` route), for a different project, expired, or unknown is rejected
with `401`/`403`.
