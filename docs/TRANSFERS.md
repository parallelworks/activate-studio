# Transfers: the server side

Transfers copy, move and sync files between the places Studio can reach. The server side is the Go package `server-go/internal/transfers`, registered as the `transfers` track (`internal/server/tracks_transfers.go`). This page describes how it works; the page and its help are documented with the web app.

## Locations

A location is a place files live, by id `<kind>:<name>`.

| Kind | What it is | Offered |
| --- | --- | --- |
| `library` | A library's files: the main folder, a folder on this computer, a library's own host, or the host its originals are read through (sign-in off only) | Always; writable only for the main library |
| `computer` | Downloads, Desktop and Documents (those present and switched on), `STUDIO_TRANSFER_FOLDERS`, and folders added in Settings | Sign-in off; with sign-in on, `STUDIO_TRANSFER_FOLDERS` only |
| `cluster` | The viewer's active ACTIVATE clusters, rooted at `/`, browsing from the home folder, over `pw ssh` with the viewer's key | When the viewer has a platform key |
| `saved` | A host and folder saved in Settings, Transfers | Sign-in off only |
| `host` | The home folder of each host a remote library is reached on, found with no setup; switched off or hidden per host in Settings | Sign-in off only |
| `conduit` | Paths in a CONDUIT service's namespace, typed by hand | Sign-in off, when CONDUIT is switched on |

Paths are relative to the location's root. `""`, `"."` and `"/"` all mean the root. Hidden entries (a name starting with `.`) and symbolic links are never offered or moved; a plan counts them.

### Hosts that take SFTP only

A `saved` or `host` location usually runs a small bash helper on the host for each call (`fs_remote.go`). An account set up for SFTP only (OpenSSH's `ForceCommand internal-sftp`, often with a chroot) answers every command with `This service allows sftp connections only.` and status 1. On first use of a plain ssh site, Studio runs `true` there once; that answer marks the site as SFTP only for 10 minutes, and a shell answer marks it as a shell host for the same time. A helper call that gets the SFTP-only answer marks the site too and asks for a retry. Clusters reached through `pw ssh` are never probed.

For an SFTP-only site, every call opens an SFTP session with `ssh -s <destination> sftp` (`remote.Pool.Subsystem`), with the site's ssh options, port, BatchMode and connection sharing, and closes it when the call ends. The calls keep the helper's rules: hidden entries are left out of listings, links are never followed, every path's real folder must be inside the root's real folder (found by looking up each name, since some SFTP servers do not resolve links), a write never replaces a file (`O_EXCL`), landing never replaces a name the plan did not list (a hard link with `hardlink@openssh.com`, else SFTP's rename, which does not overwrite), and a staging folder is removed without following links. Free space comes from `statvfs@openssh.com` when the server offers it.

No program runs on such a host, so rsync is not offered for it and `studio-copy` carries every transfer, over SFTP. A checksum (a move, or the `checksum` option) reads each file over the session and hashes it in Studio, so the data crosses the network a second time.

## Plan, confirm, run

A plan walks the source, works out what lands where, lists what would be replaced and (sync with `deleteExtra`) deleted, and picks a mover. It is kept 15 minutes. Starting it walks again and refuses (409) when the file count grew or a replacement or deletion appeared that the plan did not show. A sync makes the destination folder match the source folder's contents, as `rsync src/ dst/` does; its change detection is Studio's own (size and modification time, or sha256 with `checksum`).

Every job writes into a staging folder `.studio-transfer-<id>` inside the destination. When the mover says it is done, Studio walks the staging folder and checks the files it expected are there with the right sizes, and with sha256 for a move or with `checksum`. A mover that reports success on a bad result fails the job and nothing is put in place. Files then take their names (a hard link where the name must not exist, a rename for a listed replacement), listed deletions run, a move removes the originals, and the main library re-indexes what changed.

## Movers

`rsync` runs on this computer for a pull or push (a pull needs rsync 3.2.5 or later here), on the host for a transfer within one host, and from one host straight to another when the destination is a plain ssh destination the source can reach. A host whose login prints text before a command runs breaks rsync's protocol, so rsync is refused there with that reason, and so is a host that takes SFTP only. `studio-copy` reads and writes each file through Studio, works between any two locations and is always on. `conduit` hands the copy to a CONDUIT service through an attached MCP server; it is only ever sent COPY, Studio verifies the result and removes originals itself, and it cannot be cancelled once started.

The mover is the one the request names, else the settings' default where it can do the transfer, else the first that can, in the order conduit, rsync, studio-copy. At most three jobs run at once.

## History

Active transfers are held in memory and in `INDEX_BASE/transfers/active.json`. A finished transfer is written once, as a line of `INDEX_BASE/transfers/<YYYY-MM>.jsonl` with its last 200 events; a deletion is a line of its own. Finished transfers are kept 180 days, at most 10,000 (`KeepFinishedFor`, `KeepFinishedMax`). At start the month files are read into an in-memory index; a transfer's events are read from its line when its detail is opened. On start, a transfer that was queued, running or verifying is marked `interrupted` and its staging folder is removed; one awaiting approval is cancelled.

## HTTP

| Route | Returns |
| --- | --- |
| `GET /api/transfers/locations` | `{ locations }` |
| `GET /api/transfers/browse?location=&path=` | A listing; without `path`, a cluster starts at its home folder |
| `POST /api/transfers/plan` | `{ plan }` |
| `POST /api/transfers` with `{ planId }` | `{ transfer }`, once queued |
| `GET /api/transfers?state=active\|done\|all&limit=&before=&q=&outcome=&startedBy=&since=` | `{ transfers, next }`: active ones first, never paged, then finished ones newest first by end time; `next` is an opaque cursor or null; `limit` 1 to 200, default 50 |
| `GET /api/transfers/summary` | `{ active, awaitingApproval, failedLast24h }` |
| `GET /api/transfers/movers?source=&destination=` | `{ movers }`: which movers can reach between two locations |
| `GET /api/transfers/{id}` | `{ transfer }` with `events` |
| `POST /api/transfers/{id}/cancel` | `{ cancelled, why? }` |
| `DELETE /api/transfers/{id}` | `{ deleted }` for a finished one |
| `GET`, `PUT /api/transfers/settings` | The Transfers settings (below) |
| `POST /api/transfers/settings/test` | `{ ok, why?, rsync?, rsyncVersion?, writable?, freeBytes?, roundTripMs?, sftpOnly? }` |

Errors are `{ "error": "..." }` with the status. With sign-in on every route acts for the signed-in user only, and another user's transfer is a 404.

## Settings

Stored under the `transfers` key of the settings file:

```
{ savedLocations: [{ id, label, host, root, rsyncPath?, port?, sshOptions? }],
  computerFolders: [{ path, label, builtIn, enabled }],
  movers: { rsync: { enabled, path }, studioCopy: { enabled: true },
            conduit: { enabled, server, mounts: [{ location, path, conduit }] } },
  defaults: { mover, checksum, preserveTimes, parallel },
  hostLocations: { enabled, hidden: [host] } }
```

GET adds `managed`, `mcpServers` and `detected: { rsyncLocal, rsyncVersion?, clusters, libraryHosts }`. A PUT is validated field by field and a 400 names the field (`savedLocations[0].root: ...`). With sign-in on the section is `managed`, PUT and the test route are 403, and the values come from the environment: `STUDIO_TRANSFER_FOLDERS`, `STUDIO_RSYNC` (a path, or `off`), `STUDIO_TRANSFER_MOVER` and `STUDIO_TRANSFER_STREAMS`.

The local rsync is the configured name on `PATH`, then `/usr/bin`, `/bin`, `/usr/local/bin`, `/opt/homebrew/bin` and `/opt/local/bin`, so a desktop launch with a short `PATH` still finds it. `ssh` and `pw` are found as the remote libraries find them.

## For the chat

`transfers.Of(app)` is the service the chat package calls: `Locations`, `Browse`, `Plan`, `Start(ctx, viewer, planID, startedBy, conversationID)`, `Hold`, `Approve`, `Deny`, `Get`, `List`, `Summary`, `Cancel` and `OnEnd`. `Start` returns once the job is queued; the job runs apart from the request. `Hold` records an `awaiting-approval` transfer, `Approve` re-checks its plan and queues it, `Deny` cancels it. `OnEnd` callbacks receive each transfer when it ends, with its `conversationId`.

The chat's `transfers` tool group (`server-go/internal/chat/transfertools.go`) has `transfer_locations`, `browse_location`, `transfer_status` and the two that change something, `transfer_files` and `cancel_transfer`, which are held for approval. `transfer_files` plans first: a plan that cannot run goes back to the model with its reason and nothing is held. A valid plan is held with `Hold`, so the Transfers list shows it as awaiting approval, and the approval card carries the plan. **Yes** calls `Approve` and the tool returns at once with a link (`#view=transfers?transfer=<id>`); **No**, an expired hold, or a call id reused for another call calls `Deny`. When an approved transfer ends, one note is added to its conversation. A note that arrives while a reply streams there waits until the reply is stored. The chat page reads the open conversation again when one of its transfers leaves the active list, and a question sent before that is placed after the note, on the same branch.

## Tests

`go test ./internal/transfers/` covers paths, plans, staging and verification (including movers that lie), restart, per-user isolation, settings, history at 10,000 records, CONDUIT against a fake MCP server, and end to end over HTTP. `STUDIO_TEST_SSHD=1 STUDIO_TEST_SSHD_PORTS=<first>-<last>` adds a run over three real sshd processes on loopback, one of them SFTP only. The SFTP path is also tested without ssh, against `github.com/pkg/sftp`'s server over in-process pipes.
