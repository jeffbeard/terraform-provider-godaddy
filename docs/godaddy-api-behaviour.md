# GoDaddy Domains API: observed behaviour

What this project knows about how GoDaddy's v1 Domains API behaves, beyond what the published API
document says. Each entry gives its source and date. Add to it whenever behaviour is confirmed or
found to have changed.

API document: `https://developer.godaddy.com/openapi/domains-v1.json`.

The two domains used for the probes are written here as `probe-zone.example` (DNS hosted at GoDaddy) and `elsewhere-zone.example` (registered at GoDaddy, DNS hosted at Amazon Route 53). The real names are not recorded in this repository. Everything else in the quoted requests and responses is as observed.

## Inherited knowledge

From the history of this repository and its upstream, `n3integration/terraform-provider-godaddy`.
None of it has been re-verified unless a probe section below says so.

| Behaviour | Source |
|---|---|
| The API allows 60 requests per minute per credential. The client spaces requests one second apart. | `api/client.go:33-35`; v1 overview page, read 2026-11-26 |
| A type-wide replace with an empty NS or SOA list is rejected, so the client never sends one. | `api/types.go:270-273` |
| CAA writes were disabled on purpose the day after CAA support was added. The reason was not recorded. | commit `cd8cde9`, 2020-04-02, "add caa record type to disallow list to prevent propagation" |
| Custom nameservers had to be entered in GoDaddy's console before the API would accept them. | `README.md`, example comment |
| `X-Shopper-Id` is needed only by resellers managing a domain bought outside their own account. | API document |
| Bearer tokens (Personal Access Tokens) are accepted by the v1 API. | v1.10.0 release, 2026-11-25 |
| A new domain comes with a parked apex A record and `www` and `_domainconnect` CNAME records. | `plugin/godaddy/resource_dns_record.go:42-46` |
| The whole-zone record list (`GET /v1/domains/{domain}/records`) is not in the current API document, though the client uses it. | API document, read 2026-11-26 |

## Probe results

Probes are in `scripts/probe/`. Each section below is filled in from that probe's transcript.
Format for every section: the date and run ID, each question with its answer, and the request and
response that show it, copied from the transcript.

### P1: reads of things that are not there

Run 2026-11-27 02:46 UTC, run ID `1b2464`, read-only. Context for this run: `elsewhere-zone.example` is registered at GoDaddy but its nameservers pointed at Amazon Route 53 (`dig NS` returned four `awsdns` hosts), so GoDaddy held no zone file for it.

- **A group on a domain GoDaddy registers but does not serve DNS for:** `GET /v1/domains/elsewhere-zone.example/records/A/_probe-1b2464-p1-missing` returned 404, body `{"code":"UNKNOWN_DOMAIN","message":"The given domain is not registered, or does not have a zone file"}`.
- **A domain that does not exist:** `GET /v1/domains/probe-1b2464-does-not-exist.org/records/A/www` returned the same 404 and the same body. The two cases cannot be told apart from the response.
- **A domain in another account:** `GET /v1/domains/example.com/records/A/www` returned 403, body `{"code":"ACCESS_DENIED","message":"Authenticated user is not allowed access"}`.
- **An invalid token:** 401 with an empty body.
- **A token without DNS scope:** not run; no such token was supplied.
- **A group that does not exist on a zone GoDaddy does serve:** not answered. Needs a domain whose DNS is hosted at GoDaddy.

Second run 2026-11-27 02:50 UTC, run ID `3d2970`, read-only, on a zone GoDaddy serves. `probe-zone.example` has its DNS hosted at GoDaddy (`ns09`/`ns10.domaincontrol.com`). Its zone held six records: one apex A, two apex NS, and CNAMEs `imap`, `www` and `_domainconnect`.

- **A group that does not exist on a served zone:** `GET /v1/domains/probe-zone.example/records/A/_probe-3d2970-p1-missing` returned **200 with body `[]`**.
- The other three cases returned exactly what the first run saw: 404 `UNKNOWN_DOMAIN` for a domain that does not exist, 403 `ACCESS_DENIED` for another account's domain, 401 with an empty body for an invalid token.
- **Conclusion:** "the group is gone" (200, empty array) can be told apart from "no zone here" (404 `UNKNOWN_DOMAIN`), "not yours" (403) and "bad token" (401).

### P2: neighbours of a write or delete by type and name

Run 2026-11-27, run ID `6e529b`. All runs on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed after each: the six real records were unchanged.

- **Write by type and name:** `PUT /records/TXT/<name>` returned 200 with an empty body. Replacing one value with two left exactly the two new values.
- **Neighbours after the replace:** the TXT group at another name and the A group at the same name were unchanged.
- **Delete by type and name:** `DELETE /records/TXT/<name>` returned 204. A read of the group then returned 200 `[]`. Both neighbours were unchanged.
- **Delete of a group that is already gone:** 404, body `{"code":"RECORD_NOT_FOUND","message":"Record not found"}`.
- **CNAME written where a TXT group exists at the same name:** refused with 422, code `DUPLICATE_RECORD`, message "Another record with the same attributes already exists". No CNAME was created and the TXT group was intact.
- **Conclusion:** the per-name endpoints touch only their own type and name. A provider should treat 404 `RECORD_NOT_FOUND` on delete as success.

### P3: minimum TTL

Run 2026-11-27, run ID `f4f968`. All runs on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed after each: the six real records were unchanged.

- TTL 3600 and 600 were accepted (200).
- TTL 599, 300, 60, 1 and 0 were each refused with 422, body `{"code":"INVALID_BODY","fields":[{"code":"UNKNOWN_RESTRICTION","message":"must have a minimum value of 600","path":"records[0].ttl"}], ...}`.
- A refused write left the existing group as it was.
- **Conclusion:** the minimum TTL is 600 seconds.

### P4: the form of returned values

Run 2026-11-27, run ID `8a35c4`. All runs on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed after each: the six real records were unchanged.

| Written | Returned |
|---|---|
| TXT `he said "hi"; ok` | identical |
| TXT of 300 characters | identical, one record, 300 characters |
| TXT of 600 characters | identical, one record, 600 characters |
| MX data `MX1.Example.NET.` | `mx1.example.net` |
| CNAME data `Target.Example.NET.` | `target.example.net` |
| CNAME data `@` | `@` |
| MX data `@` | `@` |
| AAAA `2001:0DB8:0000:0000:0000:0000:0000:0001` | identical: not shortened, not lower-cased |
| A group with TTLs 600 and 3600 | both records, each with its own TTL |
| TXT at name `_probe-8a35c4-P4-UPPER` | name returned as `_probe-8a35c4-p4-upper` |

- **Conclusions:** GoDaddy lower-cases hostnames and names and strips a trailing dot. It stores TXT and IPv6 data exactly as written. `@` is valid CNAME and MX data. One group can hold records with different TTLs. TXT data of 600 characters is accepted, so the old client's 512-character limit (`api/types.go:227`) is stricter than GoDaddy; the real maximum was not found.
- Reads and deletes are case-insensitive on the name: the upper-case name was read and deleted through both spellings.

### P5: SRV and CAA

Run 2026-11-27, run ID `af75bb`. All runs on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed after each: the six real records were unchanged.

SRV:

- A write to `PUT /records/SRV/<name>` with `data`, `priority`, `weight`, `port`, `service` and `protocol` returned 200.
- **A read of `GET /records/SRV/<name>` with the same name then returned 200 `[]`.** The record did exist: `GET /records/SRV` listed it with `name` equal to the name written and `service` and `protocol` as separate fields.
- `DELETE /records/SRV/<name>` with the name written returned 204 and removed it.
- A write without `service` was refused: 422 `INVALID_RECORDS`, "Missing record information, [service]."
- **Conclusion:** SRV can be written and deleted by name but not read back by name. How to address an SRV group on read is unknown.

CAA:

- `data` of `0 issue "letsencrypt.org"` and of `0 issue letsencrypt.org` were each refused: 422 `INVALID_RECORDS`, "Missing record information, [flags]."
- A body with `flag` and `tag` fields was refused: 422 `INVALID_BODY`, `records[0].flag` "does not exist in the schema".
- **Conclusion:** CAA does not travel in `data` alone. GoDaddy wants a field named `flags` (and probably `tag`), which its published API document does not list. No CAA record was created, so the working format is still unknown.

### P6: nameserver change

Run 2026-11-27, run ID `8292d9`, on `probe-zone.example`. The token carried the scopes `domains.dns:update`, `domains.domain:read` and `domains.nameserver:update`. The target was a temporary Amazon Route 53 hosted zone, deleted afterwards. The zone check passed and the original nameservers were confirmed at the `.org` registry after the run.

- **Reading nameservers:** `GET /v1/domains/{domain}` returned `nameServers`. GoDaddy's own defaults came back in upper case (`NS09.DOMAINCONTROL.COM`); after the restore they came back in lower case and in the other order. Comparison must ignore case and order.
- **One nameserver:** `PATCH` refused with 422, code `MISSING_NAME_SERVER`, "At least two NS records must be specified in the request body".
- **Nameservers under `.invalid`:** refused with 422 `INVALID_BODY`, "Nameserver hostname has invalid TLD". So GoDaddy checks the hostname's form. Whether it checks that a well-formed host exists or serves the zone was not tested.
- **The change itself:** `PATCH /v1/domains/{domain}` with four Route 53 nameservers returned **204** with an empty body, after 3.6 seconds.
- **Visibility:** a read sent straight after the 204 still showed the old nameservers. So did a read at 10 seconds. The read at 20 seconds showed the new ones. The restore behaved the same way: old values at 0 and 10 seconds, new values at 21 seconds.
- **Lock:** the domain reported `"locked": true` throughout and the change was accepted.
- **Scope:** the change worked with `domains.nameserver:update` present. Whether it would be refused without that scope was not tested.
- **Conclusion:** the domain-level PATCH changes delegation. It is accepted synchronously and becomes visible to reads 10 to 20 seconds later, so a provider must wait and re-read rather than trust the first read.

### P7: paging of the whole-zone list

Run 2026-11-27 02:46 UTC, run ID `df7a6b`, read-only. Context for this run: `elsewhere-zone.example` is registered at GoDaddy but its nameservers pointed at Amazon Route 53 (`dig NS` returned four `awsdns` hosts), so GoDaddy held no zone file for it.

Not answered. Every form of `GET /v1/domains/elsewhere-zone.example/records` (no query, `limit=2` with `offset` 0, 1 and 2, `limit=500` with `offset` 1 and 2) returned 404 `UNKNOWN_DOMAIN`. Needs a domain whose DNS is hosted at GoDaddy.

Second run 2026-11-27 02:50 UTC, run ID `b235c7`, read-only. `probe-zone.example` has its DNS hosted at GoDaddy (`ns09`/`ns10.domaincontrol.com`). Its zone held six records: one apex A, two apex NS, and CNAMEs `imap`, `www` and `_domainconnect`.

- `GET /records` with no query returned all six records, status 200.
- `limit=2&offset=0` and `limit=2&offset=1` both returned the first two records. `limit=2&offset=2` returned the third and fourth.
- `limit=500&offset=1` returned all six. `limit=500&offset=2` returned `[]`.
- **Conclusion:** `offset` is a page number that starts at 1, with 0 treated as 1. It is not a count of records to skip, whatever the API document says. The old client's loop (`api/domains.go:55-78`: start at `offset=1`, add 1, stop at an empty page) is therefore correct, and audit finding F11 is not a bug.
- The list returned the apex NS records and no SOA record.
- The default `www` record came back as a CNAME with data `@`.

### P8: rate-limited responses

Run 2026-11-27 02:47 UTC, run ID `56283d`, read-only. Context for this run: `elsewhere-zone.example` is registered at GoDaddy but its nameservers pointed at Amazon Route 53 (`dig NS` returned four `awsdns` hosts), so GoDaddy held no zone file for it.

Not answered. 120 requests to `GET /v1/domains/elsewhere-zone.example/records/TXT/<name>` were sent in 27.5 seconds with no pause; every one returned 404 `UNKNOWN_DOMAIN` and none returned 429. So requests that fail with `UNKNOWN_DOMAIN` were not rate-limited at about 4 requests a second. Whether successful requests are limited at 60 a minute, and what a 429 contains, is still unknown.

Second run 2026-11-27 02:52 UTC, run ID `03b8dc`, read-only, on a served zone.

- 120 requests to `GET /v1/domains/probe-zone.example/records/TXT/<name>` were sent in 53.65 seconds with no pause. All returned 200. None returned 429.
- About 16 further requests had been sent in the two minutes before.
- **Conclusion:** with a Personal Access Token, 134 successful reads a minute were not limited. The documented limit of 60 a minute was not enforced on this endpoint in this run. What a 429 contains is still unknown, because none could be produced.

### P9: empty type-wide replace

Run 2026-11-27, run ID `c8944d`. All runs on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed after each: the six real records were unchanged. The zone had no AAAA records of its own.

- One AAAA probe record was created.
- `PUT /v1/domains/probe-zone.example/records/AAAA` with body `[]` returned **200**.
- **The probe record was still there afterwards.** A read returned it unchanged.
- **Conclusion:** an empty type-wide replace is accepted and does nothing. It does not delete the records of that type. This is the request the old resource sends for every type with no configured records, and on destroy.
- Limits of this result: one run, one type (AAAA), one record. Behaviour for other types was not tested. A type-wide replace with a non-empty array was not tested either; the API document says it replaces every record of that type.

### P10: read after write

Run 2026-11-27, run ID `2b1bf8`. All runs on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed after each: the six real records were unchanged.

- 20 writes to one TXT group, each followed at once by a read: 0 stale reads.
- 4 deletes, each followed at once by a read: 0 stale reads (each returned 200 `[]`).
- No request failed.
- **Conclusion:** reads were consistent straight after writes and deletes in this run.

### P11: record endpoints while GoDaddy does not serve the zone

Observed 2026-11-27 as a side effect of P1, P7 and P12, read-only. Context for this run: `elsewhere-zone.example` is registered at GoDaddy but its nameservers pointed at Amazon Route 53 (`dig NS` returned four `awsdns` hosts), so GoDaddy held no zone file for it.

- Every record read tried returned 404 with code `UNKNOWN_DOMAIN`: by type and name, by type (`/records/TXT`, `/records/NS`, `/records/SOA`), and the whole zone (`/records`).
- The response is identical to the one for a domain that does not exist. It differs from "another account's domain" (403 `ACCESS_DENIED`).
- So a provider can recognise this state by status 404 with code `UNKNOWN_DOMAIN`, and must not read it as "the record group is gone".
- Writes and deletes in this state were not tried.

Second observation, 2026-11-27, run ID `8292d9`: `probe-zone.example` during P6, in the minute after its nameservers were moved to Route 53.

- Every record request behaved exactly as on a served zone: read of a missing group 200 `[]`, write 200, read back 200 with the record, delete 204, type-wide read 200.
- So there are two different states, and they look nothing alike:
  1. **Nameservers elsewhere, zone file still held by GoDaddy** (`probe-zone.example`, just after the move): record endpoints work normally. Nothing in a record response shows that the zone is not being served. Only `GET /v1/domains/{domain}` reveals it, through `nameServers`.
  2. **No zone file at GoDaddy** (`elsewhere-zone.example`, on Route 53 for a long time): every record endpoint returns 404 `UNKNOWN_DOMAIN`.
- How long GoDaddy keeps the zone file after nameservers move away, and whether state 1 always turns into state 2, is not known.

### P12: type-wide and whole-zone reads

Run 2026-11-27 02:46 UTC, run ID `f7f269`, read-only. Context for this run: `elsewhere-zone.example` is registered at GoDaddy but its nameservers pointed at Amazon Route 53 (`dig NS` returned four `awsdns` hosts), so GoDaddy held no zone file for it.

Not answered. `GET /records/TXT` (with and without `limit` and `offset`), `/records/NS`, `/records/SOA` and `/records` all returned 404 `UNKNOWN_DOMAIN`. Needs a domain whose DNS is hosted at GoDaddy.

Second run 2026-11-27 02:51 UTC, run ID `b5c681`, read-only. `probe-zone.example` has its DNS hosted at GoDaddy (`ns09`/`ns10.domaincontrol.com`). Its zone held six records: one apex A, two apex NS, and CNAMEs `imap`, `www` and `_domainconnect`.

- `GET /records/{type}` works: `/records/NS` returned the two apex NS records, `/records/TXT` returned `[]`, both status 200.
- `GET /records/SOA` returned `[]`, status 200: the SOA record is not exposed.
- `GET /records` returned the whole zone, status 200.
- Paging on the per-type read could not be tested: the zone had no type with more than two records.

### P13: type-wide replace with a non-empty array

Added after P9's result. Run 2026-11-27, run ID `a85a9c`. Run on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed: the six real records were unchanged. The zone had no AAAA records of its own.

- Two AAAA probe records were created at two names, `p13-a` and `p13-b`.
- `PUT /v1/domains/probe-zone.example/records/AAAA` with a body holding one record, for `p13-b`, returned 200.
- Afterwards the group at `p13-a` read as `[]`, and `GET /records/AAAA` listed only the new `p13-b` record.
- **Conclusion:** a type-wide replace with at least one record deletes every other record of that type, at every name. Together with P9: the old resource deletes records of a type only when the configuration holds at least one record of that type. A type with no configured records is left alone.

### P14: the forms SRV and CAA take

Added after P5's result. Run 2026-11-27, run ID `32e75d`. Run on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed: the six real records were unchanged.

SRV, written at name `N` with `service` `_sip` and `protocol` `_tcp`:

- `GET /records/SRV/N` returned `[]`.
- `GET /records/SRV/_sip._tcp.N` returned the record. Its `name` field was `N`, with `service` and `protocol` as separate fields.
- `GET /records/SRV/N._sip._tcp` and `GET /records/SRV/_sip.N` returned `[]`.
- `PUT` and `DELETE` at `/records/SRV/N` both worked (200 and 204).
- **Conclusion:** an SRV group is written and deleted at its plain name and read at `_service._protocol.name`. Whether write and delete also work at the long form was not tested, because the probe tool only writes to names that start with its prefix.

CAA:

- A record with `data` holding the value, `flags` as a number and `tag` as a string was accepted: `{"data":"letsencrypt.org","flags":0,"tag":"issue","ttl":3600}` returned 200 and read back with the same fields.
- A group of two CAA records (`issue` and `issuewild`) was accepted and read back as two records.
- Read by name, read by type and delete by name all worked at the plain name.
- **Conclusion:** CAA works through the per-name endpoints with two extra fields, `flags` and `tag`, that the v1 API document does not list. The v3 document names the first one `flag`; v1 refuses `flag` and requires `flags`.

### P15: what one SRV group is

Added after P14. Run 2026-11-27, run ID `78191a`. Run on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed: the six real records were unchanged. `N` is the plain record name; two services were used, `_sip._tcp` and `_xmpp-client._tcp`.

- **A write at the plain name replaces every SRV record at that name, whatever its service.** With the `_sip` record in place, `PUT /records/SRV/N` carrying only the `_xmpp-client` record left only the `_xmpp-client` record.
- A write at the plain name carrying both records left both.
- `GET /records/SRV/_sip._tcp.N` returns only the `_sip` records at `N`; `GET /records/SRV/_xmpp-client._tcp.N` only the `_xmpp-client` ones. `GET /records/SRV/N` returns `[]` (P14). `GET /records/SRV` lists them all, each with `name` equal to `N`.
- **A write at the long form is a different record.** `PUT /records/SRV/_sip._tcp.N` returned 200 and created a new record whose `name` is literally `_sip._tcp.N`, beside the existing ones at `N`. `DELETE /records/SRV/_sip._tcp.N` returned 204 and removed only that new record.
- `DELETE /records/SRV/N` returned 204 and removed every SRV record at `N`, both services.
- **Conclusion:** for writing and deleting, one SRV group is all SRV records at one name, across services, addressed by the plain name. There is no single request that reads exactly that group: it has to be read from the type-wide list and filtered by name, or assembled from one long-form read per service.

### P16: under-scoped tokens

Added to close the "token without scope" cases of P1 and P6. Run 2026-11-27, run ID `0310cb`. Run on `probe-zone.example` (DNS hosted at GoDaddy). The zone check passed: the six real records were unchanged. The nameservers were unchanged afterwards.

Token with `domains.domain:read` only:

- Read of a record group, read of a whole type, read of the domain: all 200.
- Write of a record group: **401, empty body.** Delete: 401, empty body.

Token with `domains.domain:read` and `domains.dns:update`:

- Read of the domain: 200. Write and read of a record group: 200.
- Nameserver `PATCH` (sent with the nameservers the domain already had, so that it could change nothing): **401, empty body.**

- **Conclusions:** reading records and the domain needs only `domains.domain:read`. Writing records needs `domains.dns:update`. Changing nameservers needs `domains.nameserver:update`. A missing scope is answered with 401 and an empty body, exactly like an invalid token (P1), not with 403. A client cannot tell the two apart.

### P17: type-wide replace on TXT, MX and SRV

Added to check whether P9 and P13, which used AAAA only, hold for other types. Run 2026-11-27, run ID `5d6488`, on `probe-zone.example`. The zone check passed: the six real records were unchanged. The zone had no TXT, MX or SRV records of its own. For each type, two probe records were created at two names.

| Type | `PUT /records/{type}` with `[]` | `PUT /records/{type}` with one record for the second name |
|---|---|---|
| TXT | 200; 2 records before, 2 after | 200; the first name's record was deleted |
| MX | 200; 2 records before, 2 after | 200; the first name's record was deleted |
| SRV | 200; 2 records before, 2 after | 200; the first name's record was deleted |

- **Conclusion:** for AAAA (P9, P13), TXT, MX and SRV, an empty type-wide replace returns 200 and deletes nothing, and a type-wide replace with at least one record deletes every other record of that type.
- Not tested: A and CNAME, because the probe zone has real records of both types. NS, SOA and CAA are never sent type-wide by the old resource with an empty list.

### P18: type-wide replace on A and CNAME

Added to close the two types P17 could not cover, because the probe zone has real records of both: one apex A, and CNAMEs `imap`, `www` and `_domainconnect`. The maintainer agreed to have those removed and put back. Run 2026-11-27, run ID `0b86a7`, on `probe-zone.example`. The real records were restored with one type-wide write per type, and the zone check passed: the six real records were identical afterwards.

| Type | `PUT /records/{type}` with `[]` | `PUT /records/{type}` with one probe record |
|---|---|---|
| A | 200; 3 records before, 3 after | 200; the other probe record and the real apex A record were deleted |
| CNAME | 200; 5 records before, 5 after | 200; the other probe record, `imap` and `www` were deleted; **`_domainconnect` was still there** |

- **Conclusion:** the rule holds for all six types the old resource writes (A, AAAA, CNAME, MX, SRV, TXT): an empty type-wide replace returns 200 and deletes nothing, and a type-wide replace with at least one record deletes the other records of that type.
- **One exception:** the `_domainconnect` CNAME survived a CNAME replace that did not include it. GoDaddy either protects that record or puts it back at once; the probe cannot tell which. Whether it can be deleted or changed through the per-name endpoint was not tested.
- Restoring by type-wide write worked for both types, including writing `_domainconnect` back in the same list (accepted, no duplicate).

### P19: changing and deleting `_domainconnect` at its own name

Added after P18, where GoDaddy's default `_domainconnect` CNAME survived a type-wide replace that left it out. Run 2026-11-27, run ID `7fd59b`, on `probe-zone.example`. The record was restored afterwards and the zone check passed: the six real records were identical.

- **Change:** `PUT /records/CNAME/_domainconnect` with a different target returned 200, and a read showed the new target.
- **Delete:** `DELETE /records/CNAME/_domainconnect` returned 204, and a read straight afterwards returned `[]`.
- **GoDaddy did not put it back:** reads at 10, 20, 30 and 60 seconds after the delete all returned `[]`.
- **Restore:** `PUT /records/CNAME/_domainconnect` with the original target returned 200 and the record read back as before.
- **Conclusion:** through the per-name endpoints `_domainconnect` is an ordinary record. It can be changed, deleted and recreated, and GoDaddy does not recreate it on its own within a minute. Its survival in P18 is a property of the type-wide replace, not protection of the record.

## What the probes mean for the 1.11.0 design

Compared with `docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md` on 2026-11-27. "Needs spec change" lines come first.

- P5, P14: needs spec change. CAA cannot travel in `data` alone; it needs `flags` (number) and `tag` (string), which the record block does not have. SRV is read at `_service._protocol.name` but written and deleted at the plain name, so the spec's single path per group does not hold for SRV, and what defines one SRV group (name, or name plus service and protocol) is still open.
- P9, P13: needs spec change. Appendix A findings F1, F2 and F3 overstate the damage. An empty type-wide replace is a no-op, so a type with no configured records is left alone and destroy does not wipe A, MX or TXT. A type with at least one configured record does lose every other record of that type. The old resource also cannot delete the last record of a type, which no finding mentions.
- P7: needs spec change. Finding F11 is wrong: `offset` is a one-based page number and the old client's paging is correct. Decision D9 and the pagination paragraph can be simplified.
- P11: needs spec change. Read case 3 must name both states: 404 `UNKNOWN_DOMAIN` is an error, never "gone"; and a zone whose nameservers have moved but whose zone file remains cannot be detected from record responses at all. Whether `godaddy_dns_record` should check the domain's nameservers and warn is a decision the spec has not made.
- P8: needs spec change. No 429 could be produced at 134 requests a minute, so the format of a rate-limited response is still unknown and "Retry" cannot name a header or field. The one-request-per-second rule rests on a documented limit that was not observed; GoDaddy's own pages disagree about it.
- P4: needs spec change, small. GoDaddy stores IPv6 as written, so the read side must do the conversion to canonical form itself. TXT of 600 characters is accepted; the spec needs a stated limit and the real maximum was not found. The rest is confirmed: hostnames come back lower case without a trailing dot, `@` is valid CNAME and MX data, and one group can hold mixed TTLs.
- P6: confirmed, with numbers the spec lacked. The PATCH returns 204 and is visible to reads after 10 to 20 seconds, which fits the spec's 10 second poll. Nameserver comparison must ignore case and order. A second nameserver-resource question is now answered for free: `nameServers` is present in the domain response.
- P1: confirmed. A missing group (200 `[]`) can be told apart from a missing zone (404 `UNKNOWN_DOMAIN`), another account's domain (403) and a bad token (401).
- P2: confirmed. Writes and deletes by type and name leave every other record alone. Delete of a missing group returns 404 `RECORD_NOT_FOUND`, to be treated as success. A CNAME that conflicts with existing records is refused with 422 and changes nothing.
- P3: confirmed. Minimum TTL is 600.
- P10: confirmed. No stale reads after 20 writes and 4 deletes, so "Reading after a write" needs no polling for records.
- P12: confirmed, nothing in 1.11.0 depends on it. Per-type and whole-zone reads work, so a later release can read a zone in fewer requests.
- P19: confirmed. `_domainconnect` behaves like any record at the per-name endpoints, so the new resource needs no special case for it.
- P17, P18: confirmed. P9 and P13 hold for all six types the old resource writes. Needs a small spec change: the statements no longer need the "inferred for A and CNAME" qualifier, and the `_domainconnect` CNAME is an exception that survives a type-wide replace.
- P15: needs spec change. One SRV group is all SRV records at a name, across services; it is written and deleted at the plain name and can only be read through the type-wide list. With that, SRV can be supported.
- P16: needs spec change. A missing scope returns 401 with an empty body, the same as an invalid token, so error messages cannot name the cause with certainty. Scopes per operation are now known: read needs `domains.domain:read`, record writes `domains.dns:update`, nameserver changes `domains.nameserver:update`.

