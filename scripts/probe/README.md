# GoDaddy API probes

`go run ./scripts/probe -probe <name>` sends a fixed set of requests to the GoDaddy Domains API and
writes a transcript to `scripts/probe/out/` (not committed). The probes answer the questions in
"Phase 0: live probes" of `docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md`. Results
are written up in `docs/godaddy-api-behaviour.md`.

**These probes write to a live DNS zone. Use a domain that nothing depends on.**

## Setup

```bash
export GODADDY_API_TOKEN=...            # Personal Access Token
export GODADDY_PROBE_DOMAIN=example.org # no default; you must choose
```

P16 alone needs two more tokens, and does not run without both:

```bash
export GODADDY_PROBE_READONLY_TOKEN=... # scope domains.domain:read only
export GODADDY_PROBE_DNSONLY_TOKEN=...  # scopes domains.domain:read and domains.dns:update
```

## What keeps the zone safe

- Every record a probe creates is named `_probe-<run id>-...`. The tool refuses to send a write to
  any other name; this is enforced in `guard.go` and tested. There is one exception, for SRV only:
  the long form of such a name, `_service._protocol._probe-<run id>-...`, which is the same record
  under the name GoDaddy reads it back by (P15).
- Before and after each writing probe the tool reads every record of every type and compares the
  two readings record by record, all fields included. If any record outside the prefix was added,
  removed or changed, the run fails and the transcript ends with the difference.
- Only P9, P13, P17 and P18 may replace a whole record type: P9 and P13 only AAAA, P17 only TXT, MX
  and SRV, P18 only A and CNAME. P9 and P13 do not run, and P17 skips a type, when the zone has a
  record of that type of its own. P18 is the exception; see below. Only P6 may change the
  nameservers. P16 sends one nameserver PATCH that repeats the nameservers the domain already has,
  read one request earlier, and then checks that they are unchanged; the run fails if they are
  not. All six need `-allow-zone-wide`. A write that replaces the whole zone is never sent.
- P9 and P13 do not run if the zone has an AAAA record of its own.
- P6 reads the original nameservers first and changes nothing if it cannot. However it ends, it
  puts them back: it sends the originals (up to 20 times if GoDaddy refuses), then waits until the
  domain shows them. If it cannot, the run fails and the last line of the
  transcript names the nameservers to set by hand in the GoDaddy console.
- A redirect is never followed.
- Contact details in the domain response are removed before anything is written to the transcript.
  A domain response the tool cannot parse is withheld whole.
- No token is ever written anywhere.

P18 is the one probe that removes real records. It replaces all A records, then all CNAME records,
and if GoDaddy behaves as it does for other types, the zone's own A and CNAME records are gone for
a short time. It needs `-replace-real-records` as well as `-allow-zone-wide`. What it does to put
them back:

- Before the first write for a type it prints that type's real records, with every field, as JSON
  under "Real <type> records, to recreate by hand if the restore fails".
- It replaces nothing unless a fresh read of the type shows exactly those records.
- However its work on a type ends, Ctrl-C included, it reads the type, and if any real record is
  missing or differs it sends them all back in one request (up to 20 times if GoDaddy refuses) and
  reads again. It finishes A, restore included, before it touches CNAME.
- If it cannot show that every real record is back, the run fails, CNAME is not started, and the
  last line of the transcript repeats the JSON.
- The zone check at the end of the run compares the whole zone with the snapshot once more.

P19 touches one real record in the same way: it changes and deletes the `_domainconnect` CNAME at
its own path, the only real record the guard lets any probe write by name, watches for a minute to
see whether GoDaddy puts it back, and then restores it with the same steps as P18 (the record
printed first, a fresh read that must match, a verified restore, the JSON last on failure). It
needs the same two flags.

What the tool cannot promise: GoDaddy may answer a read with old data, and the tool only knows what
GoDaddy tells it. After P6, check the nameservers in the GoDaddy console as well.

## Stopping a run

Press Ctrl-C once. The tool stops sending new probe requests and then finishes safely: P6 restores
the nameservers, the probe's records are deleted, and the zone is read and compared again. Do not
close the terminal while it works. Further Ctrl-C presses are ignored.

For most probes this takes seconds. For P6 it usually takes a few minutes. In the worst case, when
GoDaddy keeps refusing the restore, it can take up to about 20 minutes: 20 attempts, each followed
by a wait of 15 seconds or of what GoDaddy asks for, up to a minute, and then a 2 minute poll.

The exit status is 130, or 1 if something also went wrong. `kill -9` and a power cut skip all of
this.

## Records left behind

The tool deletes the groups it wrote. If a record with the probe prefix is still there afterwards,
the run fails and the last lines name it; delete it by hand.

One case looks different: if GoDaddy stores an SRV probe record under a name like
`_sip._tcp._probe-...`, the name no longer starts with the probe prefix. The tool cannot delete it;
the zone check reports it as a record that changed outside the prefix and the run fails. Delete
that record by hand.

## Probes

| Probe | Writes? | Question |
|---|---|---|
| `p1` | no | What a read returns for a missing group, a missing domain, another account's domain, a bad token |
| `p7` | no | How the whole-zone list pages |
| `p12` | no | Whether type-wide and whole-zone reads work |
| `p8` | no | What a rate-limited response contains. Uses up the quota for a minute |
| `p3` | probe records | Minimum TTL |
| `p2` | probe records | Whether a write or delete by type and name touches neighbours |
| `p4` | probe records | The form values come back in |
| `p5` | probe records | SRV and CAA shapes |
| `p10` | probe records | Whether a read straight after a write or a delete is current |
| `p9` | type-wide, `-allow-zone-wide` | Whether an empty type-wide replace is accepted |
| `p13` | type-wide, `-allow-zone-wide` | Whether a type-wide replace with one record deletes the other records of that type |
| `p17` | type-wide, `-allow-zone-wide` | Whether P9 and P13 hold for TXT, MX and SRV: an empty type-wide replace, then one with a single record |
| `p18` | type-wide on real records, `-allow-zone-wide -replace-real-records` | Whether P9 and P13 hold for A and CNAME. Removes the zone's real A and CNAME records and puts them back |
| `p19` | one real record, `-allow-zone-wide -replace-real-records` | Whether the `_domainconnect` CNAME can be changed or deleted at its own name, and whether GoDaddy puts it back. Changes, deletes and restores that record |
| `p14` | probe records | The name an SRV record reads back under; which CAA body is accepted |
| `p15` | probe records | What one SRV group is: whether a write at a name replaces other services there, and whether the long name can be written and deleted |
| `p16` | nameserver PATCH that changes nothing, `-allow-zone-wide`, two extra tokens | What a read-only token and a DNS-only token are allowed to do |
| `p6` | nameservers, `-allow-zone-wide -target-ns a,b` | Nameserver change; also runs P11 |

Leave at least a minute between probes: GoDaddy allows 60 requests a minute.
