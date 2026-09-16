# Design: GoDaddy Personal Access Token (PAT) Authentication

**Date:** 2026-09-16
**Status:** Approved, not yet implemented

## Background

GoDaddy is replacing the two-part API key/secret credential (`Authorization: sso-key <key>:<secret>`)
with a single scoped Personal Access Token (`Authorization: Bearer <token>`). The legacy `sso-key`
scheme is deprecated for Domains endpoints with a sunset window through 2026. The Domains v1 endpoints
this provider uses (`/v1/domains/{domain}/records`) accept both schemes today. The `X-Shopper-Id`
header remains documented for v1, so the `customer` attribute on `godaddy_domain_record` is unaffected.

PAT scopes required for this provider: `domains.dns:update` and `domains.domain:read`
(GoDaddy requires the read scope even for write operations).

Sources:
- https://developer.godaddy.com/en/docs/api-users/auth
- https://developer.godaddy.com/en/docs/api-users/troubleshoot/authentication
- https://developer.godaddy.com/en/docs/references/rest/domains/v1

## Goals

1. Accept a PAT as the primary credential.
2. Keep existing `key`/`secret` configurations working until GoDaddy's sunset, with a deprecation warning.
3. Confine the change to the authentication path. No change to resource behavior or API endpoints.

## Decisions

| Question | Decision |
|---|---|
| Legacy support | Keep `key`/`secret`, mark deprecated. Remove in a later major version. |
| Attribute / env var | `token` / `GODADDY_API_TOKEN` |
| Both credentials present | `token` wins; `key`/`secret` ignored. No `ConflictsWith`. |
| Client internals | Precompute the `Authorization` header value once at client construction. |

## Components

### 1. Provider schema (`plugin/godaddy/provider.go`)

- Add `token`:
  - `Type: TypeString`, `Optional: true`, `Sensitive: true`
  - `DefaultFunc: schema.EnvDefaultFunc("GODADDY_API_TOKEN", nil)`
  - Description: "GoDaddy Personal Access Token. Requires scopes `domains.dns:update` and `domains.domain:read`."
- `key` and `secret`:
  - `Required: true` becomes `Optional: true`
  - Add `Sensitive: true`
  - Add `Deprecated: "GoDaddy is sunsetting API key/secret pairs. Use token instead."`
  - Env defaults (`GODADDY_API_KEY`, `GODADDY_API_SECRET`) unchanged.
- `baseurl` unchanged.
- `providerConfigure` builds `Config{Token, Key, Secret, BaseURL}` and calls `Client()`.

Note: SDKv2 applies `DefaultFunc` during validation, so the `Deprecated` warning fires whenever
`key`/`secret` are in use, including when they come only from environment variables. Env-only users
therefore see the warning on every plan/apply. The README states this.

### 2. Config (`plugin/godaddy/config.go`)

```go
type Config struct {
    Token   string
    Key     string
    Secret  string
    BaseURL string
}
```

`Client()` calls `api.NewClient(c.BaseURL, api.Credentials{Token: c.Token, Key: c.Key, Secret: c.Secret})`.

### 3. API client (`api/client.go`)

New exported type:

```go
type Credentials struct {
    Token  string
    Key    string
    Secret string
}
```

- `Validate() error`
  - Trim whitespace on all three fields.
  - If `Token` non-empty: valid. `Key`/`Secret` ignored.
  - Else if `Key` and `Secret` both non-empty: valid.
  - Else: `no credentials: set token (GODADDY_API_TOKEN) or key and secret (GODADDY_API_KEY, GODADDY_API_SECRET)`.
    Key-only or secret-only produces this same error.
- `authorization() string`
  - Token present: `Bearer <token>`
  - Otherwise: `sso-key <key>:<secret>`

`Client` struct: remove `key` and `secret` fields, add `authorization string`.

`NewClient(baseURL string, creds Credentials) (*Client, error)`: validate credentials first, then URL,
then store `creds.authorization()`.

`execute()`: `req.Header.Set(headerAuthorization, c.authorization)`. `X-Shopper-Id` handling unchanged.

### 4. Tests

- New `api/client_test.go` (unit, no network, no build tag):
  - Table test for `Credentials.Validate`: token only, key+secret, token+key+secret, key only,
    secret only, all empty, whitespace-only values.
  - Table test for `authorization()`: Bearer and sso-key outputs, whitespace trimmed.
  - `httptest.NewServer` test: construct client with token, call `GetDomains("")`, assert the server
    received `Authorization: Bearer <token>`. Repeat with key/secret asserting `sso-key k:s`.
  - `httptest` test: with `customerID` set, assert `X-Shopper-Id` still sent.
- `api/client_integration_test.go`: read `GODADDY_API_TOKEN`; fall back to key/secret. Build
  `Credentials` for each `NewClient` call. `TestAuthFailure` uses a bogus token.
- `plugin/godaddy/provider_test.go`: `testAccPreCheck` requires `GODADDY_API_TOKEN` or both
  `GODADDY_API_KEY` and `GODADDY_API_SECRET`, plus `GODADDY_DOMAIN`. `TestProvider` unchanged.

### 5. Documentation

- `README.md`: rename "API Key" section to "Authentication". Lead with PAT: where to generate
  (developer.godaddy.com), required scopes, `export GODADDY_API_TOKEN=...`, HCL `token = "..."`.
  Add a short "Legacy key/secret (deprecated)" subsection stating GoDaddy's sunset and that support
  will be removed in a future major version. Update the "Provider" paragraph to mention `token`.
- `docs/index.md`: add `token` to schema; mark `key` and `secret` deprecated.

### 6. Versioning

Additive change, existing configs keep working: minor version bump. Removing `key`/`secret` is a
separate, later major version bump.

## Error handling

- Missing credentials fail at provider configure time with the message in section 3, before any
  network call.
- Invalid or expired PAT: GoDaddy returns `401 UNAUTHORIZED`. Existing `validate()` in `client.go`
  already surfaces `[401:UNAUTHORIZED] <message>`. No change.
- Insufficient scopes: GoDaddy returns 403. Surfaced the same way. README lists required scopes.

## Out of scope

- Migrating to Domains v3 DNS endpoints. v1 accepts Bearer tokens; stay on v1.
- Honoring `429 Retry-After`. Existing 1 request/second throttle remains.
- Reducing response-body logging in `execute()`.

## Files touched

| File | Change |
|---|---|
| `plugin/godaddy/provider.go` | add `token`; `key`/`secret` optional, sensitive, deprecated |
| `plugin/godaddy/config.go` | add `Token`; pass `api.Credentials` |
| `api/client.go` | `Credentials` type; `Client.authorization`; new `NewClient` signature |
| `api/client_test.go` | new unit tests |
| `api/client_integration_test.go` | token support |
| `plugin/godaddy/provider_test.go` | precheck accepts token |
| `README.md`, `docs/index.md` | authentication docs |
