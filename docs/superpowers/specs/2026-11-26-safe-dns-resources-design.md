# Safe DNS resources for the GoDaddy provider

Date: 2026-11-26
Status: draft, revised after cross-agent review on 2026-11-26 (Appendix C) and after the live probes of 2026-11-27 (Appendix D)
Target release: 1.11.0

## Summary

`godaddy_domain_record`, the provider's only resource, claims a whole DNS zone and replaces it
type by type on every apply. For every record type that has at least one record in the
configuration, all other records of that type in the zone are deleted, with nothing in the plan to
show it. Destroying the resource replaces every CNAME in the zone with two defaults. (Measured
for all six types the resource writes; GoDaddy's own `_domainconnect` CNAME is the one record seen
to survive. See Appendix D.) This is the
design of the resource, not a defect in it, so it cannot be patched into safety.

This spec adds two new resources that can only touch what they declare:

- `godaddy_dns_record` manages one group of records: all values for one type and one name.
- `godaddy_domain_nameservers` manages a domain's nameserver delegation.

The existing resource stays in 1.x with its behaviour frozen and a deprecation warning. It is
removed in 2.0.0, which is a separate spec.

## Background

The provider is published on the Terraform Registry as `jeffbeard/godaddy` (v1.10.0). It is a fork
of the abandoned `n3integration/terraform-provider-godaddy`. Other people depend on it, and we do
not know who they are.

A code audit on 2026-11-26 found the hazards listed in Appendix A. The audit was prompted by a plan
to move a live mail domain's nameservers with this provider. That plan was dropped because a
resource with only `nameservers` set sends an empty replace for A, AAAA, CNAME, MX, SRV and TXT.
The probes later showed that GoDaddy accepts an empty replace and deletes nothing, for all six
types the resource writes (Appendix D). So that configuration would not have removed those
records, and the audit's worst case was overstated.
The per-type deletion described in the summary is confirmed.

Audit status: every code finding was verified by reading the source at commit `cab5d87`, whose Go
source is identical to tag `v1.10.0`. Nothing was run against GoDaddy. GoDaddy-side behaviour comes
from the published v1 OpenAPI document (`https://developer.godaddy.com/openapi/domains-v1.json`,
read 2026-11-26), or from the live probes of 2026-11-27 recorded in
`docs/godaddy-api-behaviour.md`, or is marked as unverified.

## Goals

1. A user can manage individual DNS records without any risk to records they did not declare.
2. A user can change a domain's nameservers without touching DNS records.
3. Upgrading from 1.10.0 to 1.11.0 does not change the requests an apply sends for any existing
   configuration. The visible differences are a deprecation warning and request pacing that is
   shared across the provider process.
4. Existing users get a clear warning and a tested migration path.
5. Every write path is covered by tests that would fail if it touched anything outside its scope.
6. What we learn about GoDaddy's API behaviour is written down in the repository.

## Non-goals

- Removing `godaddy_domain_record` or the `key`/`secret` arguments (2.0.0).
- Migrating `godaddy_domain_record` itself to the plugin framework.
- Managing NS or SOA records as DNS records (see "Record types").
- Data sources, the Domains v3 API, or a tool that generates import blocks.
- Renaming the Go module path, which is still `github.com/n3integration/terraform-provider-godaddy`.
- Terraform features that need recent versions: resource identity, write-only attributes,
  ephemeral resources, provider functions, list resources.

## Decisions

| # | Decision | Reason |
|---|---|---|
| D1 | Keep the repository; replace the resource layer in place. | The unsafe code is about 500 lines. Registry namespace, signing, release pipeline and PAT auth are sound and would have to be redone. |
| D2 | `godaddy_domain_record` behaviour is frozen in 1.x and deprecated. | Unknown downstream users. Changing delete semantics in a minor release would silently change their results. |
| D3 | One `godaddy_dns_record` manages one type+name group. | GoDaddy's per-record endpoints address a type and a name and replace that group in one call. One resource then maps to one atomic call. Same model as `aws_route53_record`. |
| D4 | Create refuses when records already exist for that type and name, and tells the user to import. | Nothing is overwritten unless the user has seen it. No override switch. |
| D5 | Nameservers are a separate resource using the domain-level PATCH. Destroy does not call GoDaddy. | A destroy that moves a live domain's delegation is its own hazard. |
| D6 | New resources use `terraform-plugin-framework`, served beside the old SDKv2 resource through `terraform-plugin-mux`, on protocol 5. | HashiCorp's guidance for new resources. Avoids writing them twice. The old resource's code is not ported. |
| D7 | Live probes against a domain nothing depends on run before resource code is written. A probe that contradicts the design removes that feature from 1.11.0. | Several GoDaddy behaviours are not stated in the API document. |
| D8 | OpenTofu is a supported, tested target. | Same protocol and artifacts; the cost is a test matrix entry and a registry submission. |
| D9 | The freeze in D2 covers the old resource's read path too. | A strict freeze is a promise unknown users can rely on. Probe P7 then showed the old pagination is correct, so there was nothing to fix. |
| D10 | Configuration values must be written in canonical form. The provider rejects other forms; it does not rewrite them. | A provider cannot plan or store a value that differs from a configured, non-computed attribute. |
| D11 | The old resource is not removed until the new resources cover every record type it can write today, or each gap is documented with an alternative. | Users must have somewhere to migrate to. |

## Architecture

```
main.go
  tf5muxserver
    ├── SDKv2 provider      plugin/godaddy/        godaddy_domain_record (frozen)
    └── framework provider  internal/provider/     godaddy_dns_record
                                                   godaddy_domain_nameservers
  both use
    api/                    HTTP client
```

### Mux

`main.go` builds a protocol 5 mux server from the existing SDKv2 provider and a new framework
provider, and serves it at `registry.terraform.io/jeffbeard/godaddy`.
`terraform-registry-manifest.json` keeps `"protocol_versions": ["5.0"]`.

Constraints that follow from this:

- Mux requires both providers to declare the same provider schema. The framework provider declares
  `token`, `key`, `secret` and `baseurl` with the same types, sensitivity, descriptions and
  deprecation messages as `plugin/godaddy/provider.go:11-43`. A unit test compares the two schemas.
- Environment variable defaults, the default base URL and credential precedence must match. The
  SDKv2 provider's configuration code (`plugin/godaddy/provider.go`, `plugin/godaddy/config.go`)
  is not changed. The framework provider resolves the same environment variables and the same
  default itself, mirroring that code.
- A mux test configures the provider from environment variables only and asserts that an old
  resource and a new resource both send the same credentials to the same base URL. The same test
  records how many deprecation warnings Terraform shows for `key` and `secret`; if the answer is
  more than one per argument, that is fixed before release.
- The new resources refuse to operate when `baseurl` is neither HTTPS nor a loopback address
  (finding F19): every create, read, update, delete and import of `godaddy_dns_record` and
  `godaddy_domain_nameservers` returns an error that says so, before any request is sent.
  Provider configuration itself still succeeds, and the SDKv2 provider and the old resource
  handle `baseurl` exactly as before. The refusal cannot sit in provider configuration, because
  both halves are configured from one provider block and an error there would break existing
  users of the old resource.
- Protocol 5 has no nested attributes. `godaddy_dns_record` therefore uses a nested block for its
  values.
- `terraform-plugin-sdk/v2` moves from v2.6.0 to the latest v2 release, which mux needs. This is the
  only change underneath the frozen resource; the characterization tests described under "Testing"
  must pass before and after the bump.

### Shared rate limit

GoDaddy allows 60 requests per minute per credential. Today each `api.Client` owns its own
one-request-per-second throttle (`api/client.go:23`, `:36-53`). With two providers in one process
there would be two clients and twice the request rate. The throttle moves to a limiter shared by
all clients in the process that use the same credential and base URL. Spacing stays at one request
per second.

Probe P8 sent 134 successful reads in a minute and was not limited, and GoDaddy's own pages give two
different limits (60 a minute; 600 per 23 minutes). One request per second is kept because it is
the stricter documented figure and the old resource has no retry. It is a conservative choice, not
an observed limit.

Consequence for users: the new resources read one group per request, so refreshing a configuration
takes about one second per `godaddy_dns_record`. A zone with 100 groups takes about 100 seconds to
plan. The documentation states this. Probe P12 checks whether a documented bulk read exists that a
later release could use; no cache is added in 1.11.0.

### Files

| Path | Purpose |
|---|---|
| `main.go` | Mux server. |
| `internal/provider/provider.go` | Framework provider, schema, configure. |
| `internal/provider/dns_record_resource.go` | `godaddy_dns_record`. |
| `internal/provider/nameservers_resource.go` | `godaddy_domain_nameservers`. |
| `internal/provider/validate.go` | Per-type validators. |
| `internal/provider/normalize.go` | Value normalisation shared by plan and read. |
| `api/records.go` | Group-level record calls. |
| `api/nameservers.go` | Nameserver read and update. |
| `api/retry.go` | Bounded retry for the new calls. |
| `api/client.go` | Changed: the throttle becomes the shared limiter; a new request path for the new calls returns typed errors and does not log bodies. The existing `execute` path used by the old resource keeps its behaviour. |
| `api/domains.go` | Unchanged. |
| `plugin/godaddy/*` | Unchanged apart from the deprecation message on the resource. |

## API client additions

All new calls use Domains v1 and the existing authentication.

| Function | Request |
|---|---|
| `GetRecordGroup(ctx, customer, domain, type, name)` | `GET /v1/domains/{domain}/records/{type}/{name}` |
| `ReplaceRecordGroup(ctx, customer, domain, type, name, records)` | `PUT /v1/domains/{domain}/records/{type}/{name}` |
| `DeleteRecordGroup(ctx, customer, domain, type, name)` | `DELETE /v1/domains/{domain}/records/{type}/{name}` |
| `GetNameservers(ctx, customer, domain)` | `GET /v1/domains/{domain}`, field `nameServers` |
| `SetNameservers(ctx, customer, domain, nameservers)` | `PATCH /v1/domains/{domain}` with `{"nameServers": [...]}` |

Rules:

- Every new call takes a `context.Context` first. It carries the logger and cancels waits and
  retries when Terraform is interrupted.
- The new calls return a typed error that carries the HTTP status code, GoDaddy's error code and
  message, the retry interval when present, and the token scope that the failed request needs
  (`domains.domain:read` for a `GET`, `domains.dns:update` for a record `PUT` or `DELETE`,
  `domains.nameserver:update` for the domain `PATCH`). The parser accepts an empty or non-JSON
  body and still returns the status code: GoDaddy's 401 has an empty body (P1, P16), and the
  existing parser fails on that with a JSON error (`api/client.go:148-150`). The existing `validate` function returns only a
  formatted string (`api/client.go:153`, `:165`), which is not enough to tell "not found" from
  "not eligible" or "too many requests".
- `type` and `name` are path-escaped. `@` is the zone apex.
- `GetRecordGroup` for type SRV does not use the path in the table, which GoDaddy answers with an
  empty array. It pages through `GET /v1/domains/{domain}/records?limit=500&offset=N` until an
  empty page and returns the records whose type is SRV and whose `name` equals the requested
  name. See "Record types".

### Retry

Every new call is idempotent, so a retry cannot compound a write.

- HTTP 429: wait for the server's stated retry interval, capped at 60 seconds. No probe could
  produce a 429, so its format is unverified. The client looks, in order, for a `Retry-After`
  header in seconds and for a `retryAfterSec` field in the JSON body (the name GoDaddy's v1 and v2
  documents use); if neither is present it waits 30 seconds.
- HTTP 5xx and network timeouts: exponential backoff starting at 2 seconds.
- At most three attempts in total, then the error is returned.
- Retries apply only to the new calls. The old resource's calls keep their current behaviour.

### Logging

New code logs through `tflog` with method, path and status. It does not log request or response
bodies at levels below trace and never logs the `Authorization` header. Logging in the old code
path is unchanged.

## Resource: `godaddy_dns_record`

```hcl
resource "godaddy_dns_record" "mx" {
  domain = "example.org"
  type   = "MX"
  name   = "@"
  ttl    = 3600

  record {
    data     = "mx1.example.net"
    priority = 10
  }
  record {
    data     = "mx2.example.net"
    priority = 20
  }
}
```

### Schema

| Attribute | Kind | Notes |
|---|---|---|
| `domain` | required, replaces on change | Registered domain name. |
| `type` | required, replaces on change | Upper case. See "Record types". |
| `name` | required, replaces on change | Relative name, `@` for the apex. Must be lower case with no trailing dot. |
| `ttl` | optional, default 3600 | One TTL for the group. Minimum 600 (probe P3). |
| `customer` | optional, replaces on change | Reseller shopper ID, sent as `X-Shopper-Id`. |
| `record` | block set, at least one | The values. |
| `record.data` | required | Non-empty, in canonical form (see "Normalisation"). |
| `record.priority` | required for MX and SRV, rejected otherwise | |
| `record.weight`, `record.port`, `record.service`, `record.protocol` | required for SRV, rejected otherwise | |
| `record.flags`, `record.tag` | required for CAA, rejected otherwise | `flags` is a number 0 to 255. `tag` is a lower-case tag such as `issue` or `issuewild`. For CAA, `data` holds the value only. |
| `id` | computed | `domain/type/name`. |

`domain`, `type`, `name` and `customer` replace the resource when changed because they identify
which group is managed. Changing one must delete the old group and create the new one, not
retarget in place (finding F10).

A field is either required or rejected for a given type, never optional, so configuration and
GoDaddy's response cannot disagree about whether it is set. On read, fields that a type does not
use are stored as null whatever GoDaddy returns for them.

### Record types

- A, AAAA, CNAME, MX, TXT and CAA are in scope.
- CAA: probe P14 showed GoDaddy accepts and returns a CAA record as
  `{"data": "<value>", "flags": <number>, "tag": "<tag>"}`, for example
  `{"data":"letsencrypt.org","flags":0,"tag":"issue"}`. A value packed into `data` alone is
  refused. Neither field is in GoDaddy's v1 API document, and the field is `flags`, not `flag`.
  The resource sends and reads exactly those fields. A resource test writes a two-record CAA group
  and reads back the same values.
- SRV is in scope, with one difference from every other type. Probes P14 and P15 showed that
  for SRV the unit GoDaddy writes and deletes is all SRV records at one name, whatever their
  service and protocol: `PUT /records/SRV/{name}` replaces them all and
  `DELETE /records/SRV/{name}` removes them all. So one `godaddy_dns_record` of type SRV manages
  every SRV record at its `name`, and each `record` block carries its own `service` and
  `protocol`. Two SRV resources for the same name are the same group and the second is refused at
  create, like any duplicate.
- SRV cannot be read at `/records/SRV/{name}`, which returns an empty array. The resource reads
  the whole-zone list, `GET /records?limit=500&offset=N` for N from 1 until a page comes back
  empty, and keeps the records whose type is SRV and whose `name` equals its own. That is the
  paging probe P7 verified. The per-type list is not used, because its paging was never tested.
  Reads by `_service._protocol.{name}` are not used either, and the resource never writes to that
  form: P15 showed such a write creates a different record with that literal name.
- NS and SOA are rejected at plan time. GoDaddy's delete-by-name endpoint does not accept them, so
  a group of that type could be created but not destroyed. Apex nameservers belong to
  `godaddy_domain_nameservers`. Subdomain delegation stays unsupported in the new resources and is
  listed under "Follow-ups".

### Operations

Every operation sends requests only to `/v1/domains/{domain}/records/{type}/{name}` for its own
`domain`, `type` and `name`, with two exceptions that are both reads: an SRV group is read with
`GET /v1/domains/{domain}/records` (paged), and the domain's nameservers are read once per domain
with `GET /v1/domains/{domain}` for the warning described under "Read". No write ever leaves the
resource's own path. This is the central invariant of the resource and is enforced by test.

**Locking**

Create, update and delete each hold an in-process lock for the whole operation. The key is
`customer/domain/type/name` with `domain` and `name` lower-cased, so two spellings of one group
share a lock.

**Create**

1. Take the lock.
2. `GetRecordGroup`.
3. If the group has any record, fail. The error lists the existing values and prints the import
   command with the ID in the form that matches the configuration, for example
   `terraform import godaddy_dns_record.mx example.org/MX/@`, or the four-part form when
   `customer` is set. The error also says that new GoDaddy domains come with default records
   (a parked apex A record and `www` and `_domainconnect` CNAMEs), which must be imported rather
   than created.
4. `ReplaceRecordGroup` with the configured values.
5. Read the group back and store what GoDaddy returned (see "Reading after a write").

If step 4 fails after its last retry, the provider reads the group once more. If the group now
equals the configured values, the write did succeed; that read is stored as the result and step 5
is not repeated. Otherwise
create returns the error and says that the group may have been written. Without this, a write that
succeeded but reported failure would leave records that the next apply refuses to overwrite.

The lock makes two resources in one configuration that declare the same group fail
deterministically: the second finds the first's records. It does not protect against a second
Terraform state or a change made in GoDaddy's console between steps 2 and 4. That window is one
request long and is documented as a known limit.

**Read**

1. `GetRecordGroup`.
2. HTTP 401 (invalid token, or a token without `domains.domain:read`) or 403 `ACCESS_DENIED`
   (the domain belongs to another account): return the error. Do not drop the resource from state.
3. HTTP 404 with code `UNKNOWN_DOMAIN`: return an error that says GoDaddy holds no DNS zone for
   this domain, either because the domain is not registered at GoDaddy or because its DNS has
   been hosted elsewhere long enough that GoDaddy keeps no zone file. Do not drop the resource
   from state. GoDaddy gives the same response for both causes, so the error names both. (A
   domain in another account is the 403 of case 2.)
4. HTTP 200 with an empty array: the group is gone. Remove the resource from state, so the next
   plan proposes to create it.
5. HTTP 200 with records: store the values and TTL in canonical form. If the records in the
   group carry different TTLs, which GoDaddy allows, store the lowest and add a warning that names the
   TTLs found. The next plan shows an update only if the configured `ttl` differs from that
   lowest value; when it is equal the plan is empty and the mix stays until the group is next
   written, which is why the warning is needed.

These responses were observed in probes P1 and P11.

One state cannot be detected here. When a domain's nameservers have been moved away but GoDaddy
still holds its zone file, the record endpoints answer exactly as for a served zone (P11). The
resource then reads and writes records that no resolver sees. That harms no record the user did
not declare, so the resource does not refuse; refusing would also block destroy, import, and
preparing records before delegation moves to GoDaddy. It warns instead. The provider reads each
domain's nameservers once per run with `GET /v1/domains/{domain}`, caches the answer in process,
and every `godaddy_dns_record` operation on that domain adds a warning when none of the
nameservers ends in `domaincontrol.com`: "this domain's nameservers are not GoDaddy's, so these
records are not being served". That is one extra request per domain, not per resource. If the
domain read fails, no warning is given and the operation continues. `godaddy_domain_nameservers`
gives the matching warning on create and update when the configured nameservers are not
GoDaddy's.

**Update**

Take the lock, `ReplaceRecordGroup` with the new values, then read back. One write call. The group
changes from the old set to the new set with no intermediate state that this provider creates.

**Delete**

Take the lock, then `DeleteRecordGroup`. GoDaddy answers 204 on success and 404 with code
`RECORD_NOT_FOUND` when the group does not exist (P2); both count as success. A 404 with code
`UNKNOWN_DOMAIN` also counts as success, with a warning that GoDaddy holds no zone for the
domain: nothing is left to delete, and an error would make the resource impossible to destroy.
A 401, a 403 or a 404 with any other code is an error.

**Reading after a write**

Create and update read the group immediately after writing and store the result. Probe P10 found
no stale read in 20 writes and 4 deletes, each followed at once by a read, so there is no polling.
Once a write has succeeded, the operation never returns without saving state, because records
that exist at GoDaddy but not in state make the next apply refuse to continue. If the read-back
differs from what was written, the provider saves what GoDaddy returned and adds a warning that
shows both; the next plan shows the difference. If the read-back itself fails, the provider saves
the values it wrote and adds a warning that the result could not be confirmed; the next refresh
corrects state.

**Renaming a resource block**

Renaming a `godaddy_dns_record` block without a `moved` block makes Terraform plan a delete of the
old address and a create of the new one for the same group, with no ordering between them. The
lock stops them interleaving, but the outcome is still either a refused create or a deleted group.
The documentation says that a rename needs a `moved` block.

**Import**

ID `domain/type/name`, or `customer/domain/type/name` for resellers. Import parses the ID, writes
`domain`, `type`, `name` and, when present, `customer` to state, and then performs only the read.
The resource's `id` is always `domain/type/name`, whichever form was imported. An ID with any
other number of parts, or with an unsupported type, is rejected.

Importing with the three-part ID and then setting `customer` in configuration plans a replace,
which deletes and recreates the group. The documentation says so, and the plan shows a warning
when a replace is caused only by `customer` changing from unset to set.

### Validation at plan time

Nothing here needs a network call, so errors appear in `terraform validate` and `plan`, before any
write.

- `type` is one of the supported types.
- `name` is `@`, or a relative DNS name whose labels are valid and whose leftmost label may be
  `*` (so `*` and `*.sub` are both accepted). A label is 1 to 63 characters of lower-case
  letters, digits, hyphens and underscores, and may start with an underscore: `_domainconnect`,
  `_dmarc` and `selector._domainkey` are ordinary names. `name` has no trailing dot, and `name`
  joined to `domain` is within the DNS length limit.
- Wherever `data` is a hostname, the same label rule applies, so underscores are accepted there
  too (GoDaddy's default `_domainconnect` record points at `_domainconnect.gd.domaincontrol.com`).
- A: each `data` is an IPv4 address. AAAA: each `data` is an IPv6 address.
- CNAME: exactly one `record`, `name` is not `@`, `data` is a hostname.
- MX: `data` is a hostname, `priority` is present and 0 to 65535.
- SRV: `priority`, `weight`, `port`, `service` and `protocol` are all present. `priority` and
  `weight` are 0 to 65535 and `port` is 1 to 65535. `service` and `protocol` are each one
  lower-case label that starts with an underscore (`_sip`, `_tcp`). `data` is a hostname or `@`.
  `name` is not itself two underscore labels and does not start with two underscore labels
  (`_sip._tcp` and `_sip._tcp.host` are both rejected), because GoDaddy keeps the service and
  protocol in their own fields. `_service._protocol.name.domain` is within the DNS length limit.
- CNAME and MX `data` may also be `@`, meaning the zone apex. GoDaddy accepts and returns that
  form (P4), and its default `www` record is a CNAME to `@`
  (`plugin/godaddy/resource_dns_record.go:44`).
- CAA: `flags` is present and 0 to 255; `tag` is present and is lower-case letters and digits;
  `data` is non-empty and is not wrapped in double quotes. The tag is not limited to a fixed
  list: only `issue` and `issuewild` were probed, and GoDaddy is left to refuse a tag it does
  not support.
- TXT: `data` is non-empty. No upper length is enforced: GoDaddy accepted 600 characters (P4) and
  its real maximum was not found. A value GoDaddy refuses fails the apply with GoDaddy's message
  and changes nothing, because the write is a single call.
- `priority`, `weight`, `port`, `service`, `protocol`, `flags` and `tag` are rejected on types
  that do not use them.
- `ttl` is at least 600, GoDaddy's minimum (P3).
- No two `record` blocks are identical.
- Values are in canonical form, as defined next.

### Normalisation

Terraform does not let a provider plan or store a value that differs from what the user configured
for a required attribute. So the provider does not rewrite configuration. It works in two halves:

- **Configuration must already be canonical.** Validation rejects a non-canonical value with an
  error that shows the canonical form to use.
- **Read converts GoDaddy's form to canonical** before storing it, so a plan after an apply is
  empty.

Canonical forms:

- `domain`, `name`, `nameservers` entries and hostname `data`: lower case, no trailing dot.
  GoDaddy returns hostnames and names in this form (P4), except its own default nameservers,
  which it can return in upper case (P6); read lower-cases them.
- Which `data` is a hostname: CNAME, MX and SRV. Lower-casing by GoDaddy was observed for CNAME
  and MX (P4) and is assumed for SRV; read lower-cases SRV `data` itself, so the assumption
  cannot cause a permanent difference. A and AAAA `data` are addresses.
- `service` and `protocol` on SRV: lower case, as validation requires.
- CAA `data` and `tag`: compared byte for byte, like TXT.
- IPv6 `data`: the shortest standard text form, lower case. GoDaddy stores IPv6 exactly as
  written and does not shorten or lower-case it (P4), so read does the conversion itself. A record
  created outside Terraform in long form therefore reads as canonical and shows no change.
- TXT `data`: compared byte for byte, no changes. GoDaddy returns it unchanged, quotes included
  (P4).

## Resource: `godaddy_domain_nameservers`

```hcl
resource "godaddy_domain_nameservers" "example" {
  domain      = "example.org"
  nameservers = ["ns-1.awsdns-01.org", "ns-2.awsdns-02.co.uk"]
}
```

### Schema

| Attribute | Kind | Notes |
|---|---|---|
| `domain` | required, replaces on change | |
| `nameservers` | required set of strings, at least two | Hostnames in canonical form: lower case, no trailing dot. |
| `customer` | optional, replaces on change | |
| `previous_nameservers` | computed set of strings | The nameservers found at create time, in canonical form. Never changes afterwards. For manual rollback. |
| `id` | computed | The domain name. |

### Operations

- **Create:** `GetNameservers`, store the result in `previous_nameservers`, then `SetNameservers`,
  then wait for the change to be visible (below). There is no existence refusal as in D4, because
  every domain always has nameservers and the plan shows the new values.
- **Locking:** create and update hold an in-process lock keyed by `customer/domain` from the
  first read to the end of the wait, so two operations on one domain in one run cannot interleave
  and record each other's nameservers as "previous".
- **Read:** `GetNameservers`. A difference from configuration shows as drift. A 401, a 403 or a
  404 `UNKNOWN_DOMAIN` returns an error and never removes the resource from state: a domain
  always has nameservers, so there is no "gone" case.
- **Update:** `SetNameservers`, then wait.
- **Delete:** no request to GoDaddy. The resource leaves state and a warning states the
  nameservers that remain in place.
- **Import:** ID is `domain`, or `customer/domain` for resellers. Import writes `domain` and, when
  present, `customer` to state, then reads. The resource's `id` is always the bare domain,
  whichever form was imported. `previous_nameservers` is empty after import.

Waiting: after `SetNameservers` the provider polls `GetNameservers` every 10 seconds, through the
shared limiter, until the returned set equals the requested set, for up to two minutes (at most 12
polls). It stops at once if Terraform cancels the operation.

On timeout the provider saves state, including `previous_nameservers`, and returns a warning, not
an error. The warning says that GoDaddy accepted the change, that it is not yet visible, and what
the previous nameservers were. Returning an error here would leave no state, the next apply would
run create again, and it would record the new nameservers as the previous ones, destroying the
rollback value. The next refresh shows whether the change has become visible. A poll that
returns an error after the PATCH was accepted is handled the same way: state is saved and a
warning is returned.

Probe P6 measured this: the PATCH returned 204, a read sent straight afterwards and a read at 10
seconds still showed the old nameservers, and a read at 20 seconds showed the new ones. The same
happened on the way back. So the first read after a change must never be trusted, and a typical
change needs two or three polls. Comparison ignores case and order.

GoDaddy rejects the change for domains that are not eligible (HTTP 409, from its API document),
for fewer than two nameservers (HTTP 422 `MISSING_NAME_SERVER`, observed) and for a hostname with
an invalid top-level domain (HTTP 422 `INVALID_BODY`, observed). Each is returned as an error with
GoDaddy's message. A domain that reports `locked: true` still accepted the change (P6).

### Token scope

Probe P16 established the scope each operation needs:

| Operation | Scope |
|---|---|
| Read records, read the domain | `domains.domain:read` |
| Write or delete records | `domains.dns:update` |
| Change nameservers | `domains.nameserver:update` |

So `godaddy_dns_record` needs the first two and `godaddy_domain_nameservers` needs the first and
the third. The documentation lists the scopes per resource.

GoDaddy answers a missing scope with HTTP 401 and an empty body, exactly as it answers an invalid
token; it does not use 403 for this. The provider cannot tell the two apart, so on a 401 its error
names both causes and the scope that operation needs, for example: "GoDaddy returned 401: the
token is invalid or lacks the `domains.nameserver:update` scope". The scope named is the one
the failed request needs, not one per operation: a create that fails on its first read names the
read scope, and one that fails on its write names the write scope. A plan can succeed with a token
that cannot apply, because reads need only the read scope; the documentation says so.

### Documentation warning

Changing nameservers does not copy records. The documentation for this resource says that the new
DNS host must already serve the zone, and that records at GoDaddy stop being authoritative once
delegation moves.

## `godaddy_domain_record` in 1.11.0

- The resource gets a deprecation message that names the two new resources and links to the
  migration guide. Terraform shows it on every plan and apply.
- Its documentation page and the README section open with a warning that describes the replace and
  wipe behaviour in plain terms.
- No change to create, update, delete, read or import, or to their requests.
- Pagination: probe P7 showed that `GetDomainRecords` (`api/domains.go:55-78`) pages correctly.
  GoDaddy's `offset` is a one-based page number, not a count of records to skip as its document
  says. Nothing is changed and no warning is needed.
- The old resource's documentation states its behaviour as measured by probes P9, P13, P17 and
  P18 on all six types it writes (A, AAAA, CNAME, MX, SRV, TXT): for each record type with at
  least one record in the configuration, every other record of that type in the zone is deleted
  on apply; a type with no records in the configuration is left alone, which also means the
  resource cannot remove the last record of a type; destroy replaces the zone's CNAMEs with two
  defaults and leaves other types alone. The documentation presents this as what GoDaddy did on
  2026-11-27, not as a guarantee: the empty-replace behaviour is undocumented and GoDaddy could
  change it, so users are still told to keep every record they need in the configuration.
- Coverage gaps: the old resource can write one thing the new resources cannot, NS records at
  subdomains (delegation). SOA is not a gap: the old resource accepts SOA blocks but never sends
  them (`api/types.go:270-273`), and GoDaddy's record endpoints do not expose SOA at all (P12).
  The deprecation message, the old resource's documentation and the migration guide name these
  cases and say that users who depend on them should stay on the old resource for now. Under D11
  the old resource is not removed while such a gap is undocumented or has no alternative.

### Migration guide

A guide at `docs/guides/migrating-from-domain-record.md` gives these steps, each with `terraform`
and `tofu` commands. The order matters: the old resource leaves state first, so that no later
step can make it write.

1. Upgrade to 1.11.0. Do not change the old resource's configuration.
2. Remove the old resource from state without destroying it, and delete its block from the
   configuration. Either run `terraform state rm`, or replace the block with a `removed` block
   that has `destroy = false`. `removed` blocks need Terraform 1.7 or OpenTofu 1.7 or later.
   From this point the records still exist at GoDaddy and nothing manages them.
3. For each type and name in the zone, write a `godaddy_dns_record`. If the old resource set
   `nameservers`, write a `godaddy_domain_nameservers`.
4. Import each new resource. Either run `terraform import` for each, or write `import` blocks.
   `import` blocks need Terraform 1.5 or OpenTofu 1.6 or later, and they take effect when
   `terraform apply` runs, not at plan.
5. Plan until the new resources show no changes other than the imports themselves, then apply.

Steps 2 to 4 can be written together and reviewed as one plan when `removed` and `import` blocks
are used: that plan must show the old resource as "removed from state, not destroyed", each new
resource as "import", and no record created, changed or deleted.

The guide states, prominently, that deleting the old block without `terraform state rm` or a
`removed` block, or running destroy, wipes the zone. It also states why the old resource must
leave state before any apply: the old resource often shows changes on every plan (finding F13),
and an apply that includes it rewrites every record type.

## Phase 0: live probes

Probes run against a domain nothing depends on, before resource code is written. They ran on
2026-11-27 against `probe-zone.example`, whose DNS GoDaddy hosts. The first choice, `elsewhere-zone.example`,
turned out to have its DNS at Route 53, so GoDaddy held no zone for it; what it returned is
recorded under P1 and P11. They are
small scripts in `scripts/probe/`, run by hand with a token in the environment. They are not part
of CI.

Record probes write only under names beginning with `_probe-` and delete what they create. Before
and after each probe run, the script snapshots the zone and fails if anything outside that prefix
changed.

| # | Question | Design depends on it for |
|---|---|---|
| P1 | What does `GET /records/{type}/{name}` return for a group that does not exist, for a domain that does not exist, and for a token without access? | Read: "gone" versus error. |
| P2 | Do `PUT` and `DELETE` by type and name leave every other record unchanged? What status codes do they return? Does `DELETE` on a missing group fail? What does GoDaddy do when a CNAME is written at a name that has other records? | The central invariant; delete idempotency. |
| P3 | What is the minimum TTL, and what error is returned below it? | `ttl` validation. |
| P4 | How are values returned: trailing dots, letter case, TXT quoting, TXT values longer than 255 characters, IPv6 form? Does GoDaddy accept and return `@` as CNAME or MX data? Can one type and name hold records with different TTLs? | Normalisation; TXT length limit; `@` data; mixed-TTL read rule. |
| P5 | For SRV and CAA: which fields does GoDaddy accept and return, and how does the `{name}` path segment relate to SRV service and protocol? For CAA: the exact `data` string format for flags, tag and value, on write and on read. | Whether SRV and CAA ship; the CAA format. |
| P6 | Nameserver `PATCH`: which token scope is required, is the change visible immediately on `GET`, what do 409 and 422 look like, is `nameServers` present in the `GET /v1/domains/{domain}` response, and does GoDaddy check that the target nameservers exist or serve the zone? | The nameservers resource. |
| P7 | `GET /v1/domains/{domain}/records?limit=&offset=`: is `offset` a record count or a page number, and does `offset=1` skip the first record? | Documentation of the old resource. Its code is not changed (D9). |
| P8 | What does a 429 response contain: header, body field, units? | Retry. |
| P9 | Does `PUT /records/{type}` accept an empty array, and what does it do? | Documentation accuracy for the old resource. Run only against types with no records outside the probe prefix. |
| P10 | After a `PUT` or `DELETE` by type and name, does an immediate `GET` of the same group return the new state? Repeat 20 times and record any stale read and how long it lasted. | "Reading after a write". |
| P11 | While the probe domain's nameservers point away from GoDaddy (during P6), repeat the P1 and P2 requests. What do read, write and delete return for a zone GoDaddy does not serve? | Read case 3; using both new resources together. |
| P12 | Do `GET /records/{type}` and `GET /records` work, are they documented, and how do they page? | Whether a later release can read a zone in fewer requests. No design in 1.11.0 depends on it. |
| P13 | Does a type-wide `PUT /records/{type}` with at least one record delete the other records of that type? Added after P9. | What the old resource really deletes (Appendix A, F1 to F3). |
| P14 | Which CAA body does GoDaddy accept, and at which path can an SRV group be read back? Added after P5. | "Record types". |
| P15 | With SRV records for two different services at one name, does a write or delete at `/records/SRV/{name}` for one service change the other? Do write and delete work at `/records/SRV/_service._protocol.{name}`? Added after P14. | What one SRV group is. |
| P16 | What do a token with only the read scope, and a token without the nameserver scope, get back? Added to close the under-scoped cases of P1 and P6. | "Token scope". |
| P17 | Do P9 and P13 hold for TXT, MX and SRV? Added after the second review. | How far the statements about the old resource can be generalised. |
| P18 | Do they hold for A and CNAME? The probe zone has real records of both, so the probe removes them and writes them back. | The same. |

P6 and P11 change the probe domain's delegation. Before they run, a hosted zone for the domain is
created at another DNS host (Amazon Route 53), and its nameservers are the target. The tool
records the original nameservers first and restores them at the end, including on failure and on
interrupt. The other zone is deleted afterwards.

Results go in `docs/godaddy-api-behaviour.md`: one section per probe, with the request, the
response, and the date. This file also records the behaviour inherited from the fork's history:
the one-request-per-second limit, that GoDaddy rejects empty NS and SOA sets, that CAA writes were
disabled on purpose in 2020, and that custom nameservers once had to be entered in GoDaddy's
console first.

Outcome rule: if a probe contradicts an assumption in this spec, the affected feature is removed
from 1.11.0 or the spec is revised and re-reviewed. Code is not written against a guess.

### Outcome

All probes ran on 2026-11-27. Results, with the requests and responses that show them, are in
`docs/godaddy-api-behaviour.md`; Appendix D lists what each one changed in this spec. Three things
remain unverified and are treated as such wherever this spec relies on them:

- The format of an HTTP 429 response. None could be produced (P8).
- Paging of the per-type record list. No type in the probe zone had enough records to test it,
  which is why the SRV read uses the whole-zone list instead.
- Whether GoDaddy's `_domainconnect` CNAME can be changed or deleted through the per-name
  endpoint. It survived a type-wide replace (P18). Until this is known, the documentation says
  that a `godaddy_dns_record` for `_domainconnect` may not be removable, and a delete that GoDaddy
  accepts but does not carry out shows as the record still existing on the next plan.

## Testing

### Characterization tests for the old resource

Written first, against `terraform-plugin-sdk/v2` v2.6.0 and with no new dependencies, before the
SDK bump and the mux change. They exist to prove that nothing in this work changes the old
resource, and they have two layers:

- **Requests.** A fake GoDaddy server (`net/http/httptest`) records every request. The tests call
  the resource's create, update, delete and read functions with resource data built by
  `schema.TestResourceDataRaw`, and assert the exact set of requests and bodies for each. Request
  order across types is random today, so the assertion is on the set, not the sequence. Import is
  covered by calling read with an ID and no `domain`.
- **Plans.** For a fixed set of state and configuration pairs, the tests compute the resource's
  diff and assert the planned changes attribute by attribute. An SDK upgrade can change planning
  without changing the requests sent for a given plan, and the request layer alone would not see
  that.

Both layers must pass unchanged after the SDK bump and after the mux change.

### Unit tests

- API client: each new call's method, path, escaping, body and error mapping; retry counts and
  waits; the shared limiter.
- Validators and normalisation: table tests per record type.
- Provider schema parity between the SDKv2 and framework providers.
- The mux test described under "Mux": environment-only configuration reaches both halves, and
  the deprecation warning count is recorded.
- Typed errors: status code, GoDaddy code and retry interval are extracted for 404, 409, 422 and
  429 responses; a 401 with an empty body and a 500 with a non-JSON body both yield a typed error
  with the right status; each error carries the scope of the request that failed.

### Resource tests against a fake server

Using `terraform-plugin-testing`, with the fake server as `baseurl`. For both new resources:

- Create, update, delete, with state checks on known values.
- Plan checks that an in-place change plans an update and an identity change plans a replace.
- Import, with verification that imported state equals created state. For
  `godaddy_domain_nameservers` the comparison ignores `previous_nameservers`, which is empty
  after import by design.
- Import with the customer-prefixed ID sets `customer` in state and sends the shopper header.
- A second plan after apply is empty.
- Drift: the fake's data is changed between steps and the plan shows it.

Specific to `godaddy_dns_record`:

- The fake server is loaded with unrelated records and fails the test on any write outside the
  resource's own type and name path, and on any read other than that path, the paged whole-zone
  list (SRV only) and the domain read. After each step the unrelated records are compared with their
  starting values.
- Create fails when the group already exists, and the error contains the import command.
- Two resources declaring the same group: one fails.
- Read of an empty group removes the resource from state; read of a missing domain returns an
  error.
- Non-canonical configuration (upper case name, trailing dot, long-form IPv6) is rejected at
  validation with the canonical form in the message.
- A field required for the type and missing, or present on a type that does not use it, is
  rejected at validation.
- A group returned with mixed TTLs is stored with the lowest.
- Read maps each response as specified: 200 with an empty array removes the resource from state;
  404 `UNKNOWN_DOMAIN`, 403 and 401 each return an error and keep it.
- Delete treats 204 and 404 `RECORD_NOT_FOUND` as success and a 404 with another code as an error.
- A CAA group of two records is written with `flags` and `tag` and reads back unchanged; a CAA
  record without `flags` or `tag` is rejected at validation.
- An AAAA record returned in long upper-case form is stored in canonical form and plans no change.
- SRV: a group with two services at one name is written in one call at the plain name and read
  back from the whole-zone list across two pages, ignoring other types and SRV records at other
  names; no request is ever sent to `/records/SRV` or to a `_service._protocol.` path.
- A read-back that differs from what was written saves what was returned and adds a warning; a
  read-back that fails saves what was written and adds a warning. Neither leaves the resource
  out of state.
- Delete with 404 `UNKNOWN_DOMAIN` succeeds with a warning.
- A group with mixed TTLs adds a warning on read.
- A domain whose nameservers are not GoDaddy's adds a warning to record operations, with one
  domain read for several resources; a failed domain read adds no warning and no error.
- With an `http://` base URL on a non-loopback host, every operation of both new resources
  returns an error and sends no request, while the old resource still works.
- Names and hostname data with underscore labels (`_dmarc`, `_domainconnect.gd.domaincontrol.com`)
  pass validation; an SRV `name` of `_sip._tcp` is rejected.
- A CAA record with `flags` 0 is sent with `"flags":0` in the body.
- Create whose write returns an error although the fake applied it ends in success; create whose
  write fails and is not applied returns the error.
- A delete and a create for the same group, started together, do not interleave.

Specific to `godaddy_domain_nameservers`:

- Destroy sends no request and emits the warning.
- `previous_nameservers` is set on create, in canonical form, and does not change on update.
- Two creates for one domain started together do not interleave.
- A poll error after an accepted change saves state and returns a warning.
- Timeout while waiting saves state with `previous_nameservers`, returns a warning, and sends no
  more than 12 polls.
- A read that still shows the old nameservers straight after the change does not end the wait,
  and nameservers returned in upper case or in a different order compare equal.

### Live acceptance tests

Run only when `TF_ACC=1`, a token, and `GODADDY_ACC_DOMAIN` are set. There is no default domain.

- Record tests use a random `_tfacc-` name prefix. The test harness snapshots the zone before and
  after and fails if any record outside the prefix changed.
- A sweeper deletes leftover `_tfacc-` groups on the configured domain.
- The nameserver test runs only when `GODADDY_ACC_NAMESERVERS=1` is also set. It restores the
  original nameservers at the end.
- The old resource is never exercised against a live domain.

### Matrix and CI

- Acceptance tests run against Terraform 1.5.7, the latest Terraform, and the latest OpenTofu.
  OpenTofu runs use `TF_ACC_TERRAFORM_PATH` and `TF_ACC_PROVIDER_HOST`. Tests do not gate on
  Terraform version numbers.
- Pull request CI runs unit tests, fake-server resource tests on the matrix, `golangci-lint`, and
  `tfplugindocs validate`.
- Live acceptance tests run from a manually triggered workflow in a protected environment that
  holds the token. They never run on pull request events.

## Documentation

- `tfplugindocs` is installed and wired to `make docs`, with `examples/` and `templates/`
  directories. CI runs `tfplugindocs validate`.
- Pages for both new resources, each with an example, the import ID formats and the required token
  scopes.
- The `godaddy_dns_record` page also covers: default records on new domains that must be imported;
  the canonical forms; that a rename needs a `moved` block; and the expected plan time of about
  one second per resource.
- The migration guide.
- `CHANGELOG.md` with a 1.11.0 entry: new resources, deprecation, and an upgrade note stating that
  the requests an apply sends for an existing configuration do not change, and listing the two
  visible differences: the deprecation warning and process-wide request pacing.
- README: the new resources are the documented default. The old resource's section moves below
  them under a deprecation heading. Commands are shown for both `terraform` and `tofu`.

## Release

- 1.11.0 through the existing process in `RELEASING.md`.
- `RELEASING.md` gains a step to submit the provider and its signing key to the OpenTofu registry
  (`opentofu/registry` on GitHub) and to confirm `tofu init` resolves the new version.

## Order of work

1. Phase 0 probes and the behaviour document. Done 2026-11-27.
2. Characterization tests for the old resource. Done 2026-11-26.
3. SDK v2 bump, mux, framework provider skeleton, shared limiter, lint.
4. API client additions and retry.
5. `godaddy_dns_record`.
6. `godaddy_domain_nameservers`.
7. Deprecation, documentation, migration guide, OpenTofu matrix, release steps.

Each step leaves the provider releasable. Steps 5 and 6 are independent of each other.

## Success criteria

- Both layers of the characterization tests pass unchanged from step 2 to release.
- No test of `godaddy_dns_record` observes a write outside its own type and name path, or a read
  other than the three the spec allows.
- Live acceptance runs leave every record outside the test prefix unchanged, on all three matrix
  entries.
- A zone managed by the old resource can be migrated by following the guide. The plan at step 5
  shows no changes for the new resources, and a snapshot of the zone taken before step 1 equals a
  snapshot taken after step 5.
- Every probe has a recorded result. The only assumptions still unverified at release are the
  three listed under "Phase 0: Outcome", and the code and documentation treat each as unknown.

## Follow-ups

- 2.0.0: remove `key` and `secret`; rename the Go module path; remove `godaddy_domain_record`,
  subject to D11.
- Subdomain NS delegation. Until it exists, D11 keeps the old resource available for those users.
- The SRV read uses the whole-zone list, which GoDaddy's v1 document does not describe. If GoDaddy
  removes it, SRV reads fail with an error; they cannot return wrong data. A move to Domains v3
  removes the dependence.
- A rate limiter that allows short bursts within GoDaddy's 60 requests per minute, to shorten
  plans. Deferred because a burst changes request timing for the old resource, which does not
  retry on HTTP 429.
- A data source for reading existing records, and a generator for import blocks.
- Domains v3.

## Appendix A: audit findings

Severity is the audit's judgement. "Addressed" describes the new resources. Every finding remains
true of `godaddy_domain_record` until 2.0.0 unless stated.

Paths: `R` is `plugin/godaddy/resource_dns_record.go`; `D` is `api/domains.go`; `T` is
`api/types.go`; `C` is `api/client.go`.

| # | Severity | Finding | Where | Addressed by |
|---|---|---|---|---|
| F1 | Critical | Create and update send `PUT /records/{TYPE}` for A, AAAA, CNAME, MX, SRV and TXT on every apply, and for NS when any NS record is configured. Of those six types, any with no configured records gets an empty array. SOA and CAA are never sent, and an empty NS set is not sent (F8). One changed record rewrites every type. **Measured effect (P9, P13, P17, P18, on all six types):** a replace with at least one record deletes every other record of that type at every name; an empty replace returns 200 and deletes nothing. So the loss is per type, and only for types the configuration uses. | `D:81-110`, `R:245-281`, `T:270-273` | One call scoped to one type and name. Empty writes rejected in the client. |
| F2 | Critical | Create does not read the zone. The plan for a new resource shows only additions. **Still true (P13):** the records lost are the other records of each type the new resource configures. | `R:245-262` | D4: create reads first and refuses. |
| F3 | Critical | Destroy sends empty replaces for A, AAAA, MX, SRV, TXT and replaces all CNAMEs with two hardcoded defaults. **Measured effect (P9, P13, P17, P18):** the empty replaces do nothing, so A, AAAA, MX, SRV and TXT records survive a destroy. Every CNAME in the zone is replaced by the two defaults. Severity is high, not critical. | `R:42-46`, `R:283-299` | Destroy deletes one group. Nameserver destroy sends nothing. |
| F4 | Critical | The resource ID is set before any write. A create that fails part-way is saved as tainted, and the next apply destroys (F3) before recreating. Standard Terraform behaviour; not reproduced. | `R:252`, `R:311` | One write per create; state is set only after it succeeds. |
| F5 | Critical | Nothing prevents two resources, or two states, from managing one domain. Each apply removes the other's records. | resource design | Resources own disjoint groups. Same-group duplicates fail at create. |
| F6 | Critical | The NS replace covers all names. Apex `nameservers` removes subdomain NS records not in configuration; a subdomain NS `record` without `nameservers` sends an NS set with no apex entries. Separately, the `ReplaceNSRecords` flag set at `R:68-70` and `R:111-113` is used only by read (`R:336`): with a subdomain NS `record` and no `nameservers`, read writes the apex NS records into `nameservers` in state, which then shows as a change. | `R:88-98`, `R:118-121`, `T:211-214`, `T:270-273`, `D:81-110` | Nameservers use the domain endpoint. NS records are out of scope. |
| F7 | High | Up to seven writes per apply in random map order, no rollback, no retry on 429. The first error leaves the zone partly replaced. | `D:82`, `D:104-106`, `C:23` | One idempotent write per operation, with bounded retry. |
| F8 | High | CAA and SOA are never written or removed, yet apply succeeds and state records them. CAA was put on the skip list deliberately in 2020 (commit `cd8cde9`, "add caa record type to disallow list to prevent propagation"). Operator precedence makes SOA always skipped too, though the comment says only empty sets. | `T:270-273` | Unsupported types are rejected at plan time. CAA is supported, with the `flags` and `tag` fields GoDaddy requires (P14). |
| F9 | High | No plan-time validation. Empty data passes because `len(data) < 0` can never be true; `README.md:121` shows `addresses = [""]`. An empty `name` passes because the name check counts labels from `strings.Split`, which is never fewer than one, and `ttl = 0` passes because only negative values are rejected. No IP, hostname or minimum TTL checks. A bad record fails at GoDaddy after other types are already replaced. | `T:222-236`, `T:124-136` | Per-type validators at plan time. |
| F10 | High | Neither `domain` nor `customer` forces replacement. Changing either retargets the resource: the new domain's zone is overwritten and the old one is abandoned. Import also cannot carry a customer: it reads with an empty `customer`, so a reseller's import sends no shopper ID. | `R:142-145`, `R:152-155`, `R:136-138`, `R:215-226` | Identity attributes replace on change. |
| F11 | Medium | Read pagination starts at `offset=1` and adds 1 per 500-record page. GoDaddy documents offset as a count of results to skip, and the list endpoint is absent from the current API document. If so, the first record is missed and an import-built configuration deletes it. The loop has no bound. Unverified. **Disproved (P7):** `offset` is a one-based page number, so the loop reads every record exactly once. This finding is withdrawn. | `D:55-78` | New resources read one group, no pagination. Old resource: probe P7 result is documented; the code is not changed (D9). |
| F12 | Medium | Read classifies apex A and NS records as `addresses`/`nameservers` only when TTL is 3600; the shorthand always writes 3600. | `T:260-268`, `T:211-219` | One explicit TTL per group. |
| F13 | Medium | Values never converge (trailing dots as in `README.md:154`, SOA and CAA in state, apex A in `record` blocks), so plans always show changes and real removals are hard to see. | `R:315-351` | Shared normalisation; tested empty second plan. |
| F14 | Medium | Removing `nameservers` from configuration does nothing at GoDaddy, because an empty NS set is skipped. State says otherwise. | `T:271-273`, `R:336-340` | Nameserver destroy is an explicit, warned no-op. |
| F15 | Medium | Nameservers are written as NS records rather than through `PATCH /v1/domains/{domain}`. Whether that changes delegation is unverified. | `D:15-17` | D5. |
| F16 | Medium | No tests cover read, create, update or delete. Unit tests cover credentials, client setup and record construction; integration tests only read. The record construction test cannot fail on its negative cases: it reports an error only when one is returned for a case expected to succeed, so a case expected to fail that succeeds passes silently. | `api/*_test.go`, `api/types_test.go:19-27`, `plugin/godaddy/provider_test.go` | "Testing". |
| F17 | Low | A merge helper discards a constructor error and would append a nil record. Unreachable today because the same validation runs earlier. | `R:123-128` | Not carried into new code. |
| F18 | Low | Request and response bodies are logged with `log.Println`. The `Authorization` header is not logged. | `D:96-97`, `C:111`, `C:132` | `tflog` in new code. |
| F19 | Low | `baseurl` accepts any scheme and any host. A wrong value sends the token over plain HTTP or to another server. | `C:168-179` | The new resources refuse to operate when `baseurl` is not HTTPS and not loopback. The old resource is unchanged. |
| F20 | Medium | The resource cannot delete the last record of a type. Removing every record of a type from the configuration sends an empty replace, which GoDaddy ignores (P9, P17, P18), while state records the records as removed. Found by probe, not by the audit. | `D:81-110` | `godaddy_dns_record` deletes a group with an explicit `DELETE`. |

## Appendix B: GoDaddy v1 API facts used

From `https://developer.godaddy.com/openapi/domains-v1.json`, read 2026-11-26.

- `PUT /v1/domains/{domain}/records/{type}`: "Replaces all DNS records of the specified type across
  all names. Records of other types are preserved." Type may be A, AAAA, CAA, CNAME, MX, NS, SOA,
  SRV, TXT. Nothing is stated about an empty array.
- `PUT /v1/domains/{domain}/records/{type}/{name}`: "Replaces all DNS records of the specified type
  and name. All other records are preserved." Same type list.
- `DELETE /v1/domains/{domain}/records/{type}/{name}`: "Deletes all DNS records matching the
  specified type and name. All other records are preserved." Type may be A, AAAA, CAA, CNAME, MX,
  SRV, TXT; NS and SOA are not accepted. Documents a 409 for domains not eligible for changes.
- `GET /v1/domains/{domain}/records/{type}/{name}` is the only documented record read. `offset` is
  "Number of results to skip for pagination". `GET /v1/domains/{domain}/records` is not in the
  document.
- `PATCH /v1/domains/{domain}` accepts `nameServers`. 422: "At least two apex (aka @) nameServers
  must be specified". 409: "The given domain is not eligible to have its nameservers changed".
- The type-wide read `GET /v1/domains/{domain}/records/{type}` and the whole-zone read
  `GET /v1/domains/{domain}/records` are not in the document but were observed working (P7, P12).
  The SRV read depends on the second.
- Rate limit: 60 requests per minute per credential (v1 overview page).
- `X-Shopper-Id` is needed only by resellers managing a domain bought outside their account.

## Appendix C: review record

On 2026-11-26 this spec was reviewed against the code by three reviewers working independently:
Codex (gpt-5.6-luna), agy (Gemini 4 Argon (High)) and Claude, which also wrote the spec. They
raised 32 findings, which merged to 21 items. One debate round settled the two claims that could
not be confirmed. None was a blocker and none was found wrong. The working files are in
`.agent-reviews/`, which is not committed.

| # | Change made to this spec | Raised by |
|---|---|---|
| 1 | Pagination in the old resource is not changed in 1.11.0; the compatibility promise is reworded (goal 3, D9). | agy, codex, claude |
| 2 | Non-canonical configuration is rejected, not rewritten (D10, "Normalisation"). | agy, claude |
| 3 | Migration removes the old resource from state first; version requirements for `removed` and `import` blocks are stated; the success criterion is revised. | claude, agy |
| 4 | Type-specific fields are required or rejected, never optional; mixed-TTL groups have a read rule. | agy, claude |
| 5 | Nameserver wait has a 10 second interval; a timeout saves state and warns. | agy, claude |
| 6 | Customer-prefixed import IDs for both resources, with state population and tests. | codex, agy, claude |
| 7 | The group lock covers create, update and delete on a normalised key; renames need `moved`. | agy |
| 8 | The SDKv2 provider's configuration code is unchanged; the framework provider mirrors it; a mux test covers environment-only configuration. | agy, claude |
| 9 | `@` as CNAME and MX data, `*.` names, combined length check. | agy |
| 10 | New client calls take a context and return typed errors; `api/client.go` is listed as changed. | agy |
| 11 | Appendix A corrections to F1, F6, F9, F10 and F16. | agy, codex |
| 12 | CAA ships only with a defined `data` format, validator and round-trip test. | codex |
| 13 | Probe P10 and "Reading after a write". | codex |
| 14 | Characterization tests have a request layer and a plan layer, written before the SDK bump. | claude |
| 15 | The old resource is not removed until its record types are covered (D11). | claude |
| 16 | Expected plan time is documented; probe P12 looks for a bulk read. A burst-capable limiter is deferred to "Follow-ups". | claude |
| 17 | Probe P11 and read case 3, for zones GoDaddy no longer serves. | claude |
| 18 | Create verifies the group after a failed write before reporting failure. | claude |
| 19 | Finding F19 on `baseurl`, with a warning in the framework provider. | claude |
| 20 | Probes P6 and P11 name their target nameservers. | claude |
| 21 | Default records on new domains are documented and named in the create error. | claude |

Items 14 to 21 came from Claude alone and have no outside confirmation.

## Appendix D: what the live probes changed

The probes ran on 2026-11-27 with the tool in `scripts/probe/`. Evidence for every line is in
`docs/godaddy-api-behaviour.md`.

| Probe | Finding | Change to this spec |
|---|---|---|
| P1 | A missing group reads as 200 `[]`; a missing zone as 404 `UNKNOWN_DOMAIN`; another account's domain as 403; a bad token as 401. | "Read" now names each response. |
| P2 | Writes and deletes by type and name leave all other records alone. Delete of a missing group returns 404 `RECORD_NOT_FOUND`. A conflicting CNAME is refused and changes nothing. | Central invariant confirmed. "Delete" names the two success responses. |
| P3 | Minimum TTL is 600. | Number filled in. |
| P4 | Hostnames and names come back lower case with no trailing dot. TXT and IPv6 are stored as written. `@` is valid CNAME and MX data. Mixed TTLs are allowed. 600-character TXT is accepted. | "Normalisation" and "Validation" updated; no TXT maximum is enforced. |
| P5, P14 | CAA needs `flags` and `tag` fields that the API document does not list. SRV is written at the plain name and read at `_service._protocol.name`. | CAA is in scope with two new attributes. |
| P15 | A write or delete at an SRV name covers every SRV record at that name, across services. A write at the `_service._protocol.name` form creates a different record. | SRV is in scope: one resource per name, read through the type-wide list. |
| P16 | Reads need only `domains.domain:read`. A missing scope returns 401 with an empty body, the same as an invalid token. | "Token scope" lists scopes per operation; 401 errors name both causes. |
| P6 | The domain PATCH returns 204 and becomes visible to reads after 10 to 20 seconds. Nameservers can come back in upper case and in any order. | Nameserver "Waiting", errors and scopes updated. |
| P7 | `offset` is a one-based page number. | Finding F11 withdrawn; D9 simplified. |
| P8 | 134 successful reads in a minute were not limited. | "Retry" and "Shared rate limit" say the 429 format and the limit are unverified. |
| P9, P13, P17, P18 | An empty type-wide replace does nothing. A replace with at least one record deletes the other records of that type. Measured on all six types the old resource writes. GoDaddy's `_domainconnect` CNAME survived a CNAME replace that left it out. | Summary, Background and findings F1 to F3 corrected; finding F20 added; the old resource's documentation describes the measured behaviour without promising it. |
| P10 | No stale read after 20 writes and 4 deletes. | "Reading after a write" has no polling. |
| P11 | With nameservers moved away but the zone file kept, record endpoints behave normally. With no zone file they return 404 `UNKNOWN_DOMAIN`. | "Read" case 3 written; the undetectable state is documented, not checked. |
| P12 | Per-type and whole-zone reads work. | None in 1.11.0. |

## Appendix E: second review record

On 2026-11-27 the spec, as amended after the probes, was reviewed again by Codex (gpt-5.6-luna),
agy (Gemini 4 Argon (High)) and Claude, against the probe evidence and the code. They raised 21
findings, which merged to 15 items; one debate round settled two. None was a blocker and none
was found wrong. All 15 were accepted.

| # | Change made to this spec | Raised by |
|---|---|---|
| 1 | SRV is read from the whole-zone list with verified paging, not the per-type list; the invariant and its tests are reworded to allow that read. | claude, agy |
| 2 | Statements about empty and non-empty type-wide replaces say which types were measured. Probes P17 and P18 were added and widened the evidence to all six types the old resource writes. | claude, agy |
| 3 | After a successful write, state is always saved; a mismatched or failed read-back becomes a warning. A poll error after an accepted nameserver change is treated like a timeout. | claude, agy |
| 4 | The error parser accepts an empty body, and each error names the scope its own request needs. | agy |
| 5 | Validation and normalisation: underscore labels, SRV ranges and label forms, `flags` and `tag` rejected elsewhere, canonical form per type, zero values sent. | agy, claude |
| 6 | CAA `tag` is not limited to a fixed list. | claude, codex |
| 7 | A cached, per-domain warning when a domain's nameservers are not GoDaddy's, instead of no check. | codex, claude |
| 8 | A lock around nameserver create and update. | codex |
| 9 | The new resources refuse an unsafe `baseurl` at operation time; the old resource is unchanged. | codex, claude |
| 10 | Delete treats 404 `UNKNOWN_DOMAIN` as success with a warning. | claude |
| 11 | SOA is not a coverage gap. | agy |
| 12 | Mixed TTLs produce a warning on read. | agy |
| 13 | `UNKNOWN_DOMAIN` wording corrected; nameserver read errors, `previous_nameservers` type and import `id` stated. | agy, claude |
| 14 | Appendix B and follow-ups note the undocumented reads. | claude |
| 15 | Order of work marks steps 1 and 2 done. | claude |

Codex first proposed refusing record operations on a zone GoDaddy does not serve (item 7) and
rejecting an unsafe `baseurl` at provider configuration (item 9). After debate it agreed to the
forms above: a warning, because such writes touch no undeclared record; and refusal in the new
resources only, because provider-level rejection would break the freeze on the old resource.

