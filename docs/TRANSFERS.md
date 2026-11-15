# Transfers: the server side

Transfers copy, move and sync files between the places Studio can reach. The server side is the Go package `server-go/internal/transfers`, registered as the `transfers` track (`internal/server/tracks_transfers.go`). This page describes how it works; the page and its help are documented with the web app.

## Locations

A location is a place files live, by id `<kind>:<name>`.

| Kind | What it is | Offered |
| --- | --- | --- |
| `library` | A library's files: the main folder, a folder on this computer, a library's own host, or the host its originals are read through (sign-in off only) | Always; writable as the next section says |
| `computer` | Downloads, Desktop and Documents (those present and switched on), `STUDIO_TRANSFER_FOLDERS`, and folders added in Settings | Sign-in off; with sign-in on, `STUDIO_TRANSFER_FOLDERS` only |
| `cluster` | The viewer's active ACTIVATE clusters, rooted at `/`, browsing from the home folder, over `pw ssh` with the viewer's key | When the viewer has a platform key |
| `saved` | A host and folder saved in Settings, Transfers | Sign-in off only |
| `host` | The home folder of each host a remote library is reached on, found with no setup; switched off or hidden per host in Settings | Sign-in off only |
| `rclone` | A remote of the user's rclone configuration (an S3 bucket, a cloud drive, an SFTP server), one location per remote, rooted at the remote's top | Sign-in off, when rclone is switched on and found |
| `conduit` | Paths in a CONDUIT service's namespace, typed by hand | Sign-in off, when CONDUIT is switched on |

### When a library is writable

The library registry marks only the main library writable. That flag means Studio edits the library through its own file operations and index. Transfers do not use it alone. A transfer writes files, so a library location is writable when Studio can reach its files with write access, and the file system's permissions decide (`LibraryWritable` in `access.go`):

| Files reached through | Sign-in off | Sign-in on |
| --- | --- | --- |
| The main folder | Writable | Writable |
| A folder on this computer (`SourceRoot`) | Writable | Read only: the account the Studio runs as is not the viewer |
| The library's own host, with the viewer's platform key | Writable | Writable: the viewer's own permissions apply there |
| The library's own host over ssh, with the server account's keys | Writable | Not offered |
| The host its originals are read through | Writable | Not offered |
| Nothing (an index only) | Not a source or a destination | Not a source or a destination |

A read-only library can still be copied from. Its location's `detail` says why it is read only.

Paths are relative to the location's root. `""`, `"."` and `"/"` all mean the root. Hidden entries (a name starting with `.`) and symbolic links are never offered or moved; a plan counts them.

### Hosts that take SFTP only

A `saved` or `host` location usually runs a small bash helper on the host for each call (`fs_remote.go`). An account set up for SFTP only (OpenSSH's `ForceCommand internal-sftp`, often with a chroot) answers every command with `This service allows sftp connections only.` and status 1. On first use of a plain ssh site, Studio runs `true` there once; that answer marks the site as SFTP only for 10 minutes, and a shell answer marks it as a shell host for the same time. A helper call that gets the SFTP-only answer marks the site too and asks for a retry. Clusters reached through `pw ssh` are never probed.

For an SFTP-only site, every call opens an SFTP session with `ssh -s <destination> sftp` (`remote.Pool.Subsystem`), with the site's ssh options, port, BatchMode and connection sharing, and closes it when the call ends. The calls keep the helper's rules: hidden entries are left out of listings, links are never followed, every path's real folder must be inside the root's real folder (found by looking up each name, since some SFTP servers do not resolve links), a write never replaces a file (`O_EXCL`), landing never replaces a name the plan did not list (a hard link with `hardlink@openssh.com`, else SFTP's rename, which does not overwrite), and a staging folder is removed without following links. Free space comes from `statvfs@openssh.com` when the server offers it.

No program runs on such a host, so rsync is not offered for it and `studio-copy` carries every transfer, over SFTP. A checksum (a move, or the `checksum` option) reads each file over the session and hashes it in Studio, so the data crosses the network a second time.

### rclone remotes

Studio uses the rclone installed on this computer and never bundles it. It looks for the program in this order: the path set in Settings, Transfers (default `rclone`); on macOS, Homebrew's `/opt/homebrew/bin/rclone` and `/usr/local/bin/rclone`; then `PATH`. It never writes rclone's configuration. It reads the remotes with `rclone listremotes --long`, which gives names and types and no secrets, and names them in rclone's own commands, so they are used as configured (`RCLONE_CONFIG` applies as usual). The remotes are read again after 30 seconds. When rclone is missing or has no remotes, Settings, Transfers and its Ready line say so, and nothing else changes.

rclone remotes are for a single-user Studio. With sign-in on there are no `rclone` locations and the rclone mover is off, since every viewer would act with the rclone credentials of the account the server runs as.

The location is `RcloneFS` (`fs_rclone.go`). Each call is an rclone subcommand run on this computer, each name its own argument after `--`, never through a shell:

| FS call | rclone |
| --- | --- |
| List, Walk | `lsjson`, `lsjson -R` (streamed, stopped at the file limit), always with `--no-mimetype --use-server-modtime` |
| Stat | `lsjson --stat` for a few paths, a listing of their folders for many |
| Read, Write | `cat`; `rcat`, with the modification time set as metadata in the same upload, after checking the name is free |
| Mkdir, Free | `mkdir`; `about --json` where the backend reports space |
| Sha256 | `lsjson --hash --hash-type sha256` where the backend has sha256, else the file read through `cat` and hashed here |
| Land | one `move --files-from-raw` of exactly the staged files, after checking each name (only a listed replacement may be written over) |
| Remove | one `delete --files-from-raw` with `--max-delete` set to the number of files, then `rmdir` of each listed folder, deepest first |
| RemoveStaging | `purge` of a folder whose name is a staging folder's |

Paths are relative to the remote's top and checked like every other location's, so no path names a place above it. On rclone's local backend links are skipped, as rclone does without `-L`. A remote's features (`rclone backend features`) are read once and kept ten minutes: its hashes, whether it renames on the server, whether it reports free space, and whether it can hold an empty folder.

The listing flags matter on object stores. Without `--no-mimetype` and `--use-server-modtime`, rclone makes one request per object to read its type and stored time, and a listing of 20,000 S3 objects takes minutes; with them it takes about two seconds. With `--use-server-modtime` an object's time is when it was uploaded, not the file's own modification time.

Object stores (S3, GCS, Azure Blob and others) have no folders: a folder is the common start of object names. There `Mkdir` makes nothing below a bucket, a path that holds no objects lists as empty and can be a destination, a folder disappears with its last object, so `Remove` has no folders to remove, and empty folders are never transferred.

## Plan, confirm, run

A plan checks that the account can write in the destination folder, and for a move in the folders the originals leave: the folder holding each item named and each folder named, at most 8. It makes a folder named `.studio-transfer-probe-<id>` there and removes it, as the Settings connection test does. This works the same on this computer, over a shell on a host, and over SFTP. On an rclone remote with no real folders (an object store), making a folder writes nothing, so the check writes a zero-byte object named `.studio-transfer-probe-<id>` there and deletes it. A bucket read with read-only or anonymous credentials is then refused at the plan, not when the transfer runs. If the probe cannot be removed, a warning is logged and the plan goes ahead. Error messages name a folder on a remote as rclone does, such as `archive:bucket/reports`. When the folder cannot be made, the plan is refused with a 400 such as `you cannot write in /data/ref/reports on files.example.com (Reference), so nothing can be put there`. Folders deeper in a move are not checked. If one of them cannot be written, the originals there stay in place, the copy at the destination is kept, and the transfer fails with the count of originals not removed.

A plan walks the source, works out what lands where, lists what would be replaced and (sync with `deleteExtra`) deleted, and picks a mover. It is kept 15 minutes. Starting it walks again and refuses (409) when the file count grew or a replacement or deletion appeared that the plan did not show. A sync makes the destination folder match the source folder's contents, as `rsync src/ dst/` does; its change detection is Studio's own (size and modification time, or sha256 with `checksum`). When one end is an rclone remote, a sync compares size and a hash both ends have (MD5 first, which an object store gives from its listing at no cost; this computer reads its own files to compute it), and size and modification time when the ends share none or a file has none (an S3 object uploaded in parts has no MD5). Because an object store lists upload times, files of the same size are then sent again. The plan says which rule it used (`compare`).

Every job writes into a staging folder `.studio-transfer-<id>` inside the destination. When the mover says it is done, Studio walks the staging folder and checks the files it expected are there with the right sizes, and with sha256 for a move or with `checksum`. A mover that reports success on a bad result fails the job and nothing is put in place. Where one end is an rclone remote, the check for a move or with `checksum` uses a hash both ends have in place of sha256 (on S3, MD5 from the listing, so nothing is downloaded again); a file with no such hash is checked with sha256. Files then take their names (a hard link where the name must not exist, a rename for a listed replacement), listed deletions run, a move removes the originals, and the main library re-indexes what changed.

Only the main library is re-indexed after a transfer. For any other library a transfer writes into or moves out of, the plan and the transfer carry `indexNotes`, such as "The index of Reference shows this change after its next rebuild in Settings, Indexing, Libraries" for a library Studio builds, or "after the index is rebuilt where it is built" for an index connected as it is. The finished transfer's events repeat the note.

## Movers

`rsync` runs on this computer for a pull or push, on the host for a transfer within one host, and from one host straight to another when the destination is a plain ssh destination the source can reach. Which rsync releases it accepts is below. A host whose login prints text before a command runs breaks rsync's protocol, so rsync is refused there with that reason, and so is a host that takes SFTP only. `studio-copy` reads and writes each file through Studio, works between any two locations and is always on. `conduit` hands the copy to a CONDUIT service through an attached MCP server; it is only ever sent COPY, Studio verifies the result and removes originals itself, and it cannot be cancelled once started.

`rclone` runs `rclone copy` on this computer when one end is an rclone remote and the other is this computer or another remote. It is given the plan's file list with `--files-from-raw`, so it sends exactly what the plan lists:

```
rclone copy --files-from-raw <list> --no-traverse --use-json-log --stats 1s --stats-log-level NOTICE \
  --transfers <n> --checkers <n> [--ignore-existing | --ignore-times] -- <source folder> <destination folder>
```

It is always `copy`, for a move and a sync too: Studio decides what goes, checks what arrived, then removes originals or deletes extras itself. `rclone move` would delete originals before that check, and `rclone sync` would delete what its own listing found extra rather than what the plan showed. Progress comes from rclone's JSON stats line, once a second (bytes and files done). Cancel sends rclone an interrupt, which aborts uploads in parts, and kills it after 15 seconds. A transfer between an rclone remote and a cluster or ssh host uses `studio-copy`, with rclone reading or writing the remote through `cat` and `rcat`. Studio never configures an rclone remote to reach a host. For ssh hosts, a location test also reports whether rclone is on the host (`command -v rclone`), for information only.

Staging and object stores: where the destination remote can rename on the server (a file system, SFTP, most drives), rclone writes into the staging folder and files take their names after the check, as with every mover. On a remote that cannot (object stores), giving a staged object its name is a server-side copy of every byte and a delete, so the rclone mover writes straight to the final names instead. What is then guaranteed: an object store shows an object only once its upload is complete, so nothing appears half written; files the plan does not list as replaced are sent with `--ignore-existing`, so a name that appeared since the plan is not written over; listed replacements are sent with `--ignore-times`; Studio then checks each file's size (and hash, for a move or with `checksum`) at its final name before it removes originals or deletes extras. What is not: if the transfer fails or is cancelled, the objects already sent stay, and a replaced object is replaced when its new copy arrives, not after the check. The plan says this in its warnings. `studio-copy` into an object store (from a cluster or ssh host) still uses a staging folder.

The mover is the one the request names, else the settings' default where it can do the transfer, else the first that can, in the order conduit, rsync, rclone, studio-copy. rsync does not reach rclone remotes. At most three jobs run at once.

### rsync versions

Studio uses the rsync installed on each end, and the two ends often run different releases: macOS ships rsync 2.6.9 or openrsync, RHEL 8 ships 3.1.3, and a current Linux has 3.2 or later. rsync agrees a protocol version with the far end, but options are not negotiated, so an option the far end does not know makes it fail. Studio therefore reads each end's rsync once (`--version`, `--help` and a `--trust-sender --version` probe, kept five minutes; a host's through the helper) and gives each option only when the end or ends that must understand it have it. The plan's reason says why rsync was or was not picked.

The rsync that runs the command needs `--files-from`, `--from0` (or `-0`) and `--chmod`; without one, rsync is not used for that transfer and the reason names the option. The file names travel only in the `--files-from` list, never on a command line. What else is given follows what rsync passes to the far end (`server_options()` in rsync's `options.c`):

| Option | Needed on | Given when |
| --- | --- | --- |
| `-s` (`--protect-args`, or `--secluded-args` in newer releases) | both ends | both have it, for a pull, push or host-to-host send |
| `--timeout=120` | both ends | both have it |
| `--max-size` | the receiving end | the receiver has it |
| `--no-inc-recursive` | the running rsync only | it has incremental recursion (3.0 and later) |
| `--info=progress2` | the running rsync only | it has `--info` (3.1 and later); otherwise progress is reported at the end |
| `--chmod`, `--files-from` | the running rsync only | always; it applies them itself |
| `--files-from=-`, `--from0` | the sending host, in a pull | always; a pull needs both there |

Without protect args, the folder on the far end passes through the far end's shell. Studio then quotes it for a POSIX shell (in single quotes) when the running rsync passes it on as it is (2.6.9 to 3.2.3, and openrsync), or escapes only its wildcard characters when the running rsync escapes the rest itself (3.2.4 and later). `RSYNC_OLD_ARGS` and `RSYNC_PROTECT_ARGS` are removed from rsync's environment so they cannot change this.

Each kind of transfer then works as follows.

| Transfer | rsync needed |
| --- | --- |
| Within this computer | any rsync with the options above, 2.6.9 and openrsync included |
| This computer to a host | the same here; any release on the host |
| A host to this computer | here, rsync 3.2.5 or later, or a build that accepts `--trust-sender`; any release on the host that has `--files-from` and `--from0` |
| Within one host | the host's rsync with the options above |
| One host to another | the source host's rsync with the options above; any release on the destination |

A pull needs the fix for CVE-2022-29154 on this computer: before it, an rsync client trusts the file list the sending host returns, and a malicious or compromised host can write files outside the destination folder. Upstream fixed this in 3.2.5 and added `--trust-sender` in the same change, so a build that accepts `--trust-sender` is taken to carry the fix. RHEL 8's rsync 3.1.3 carries the fix from 3.1.3-18 (August 2022) but accepts `--trust-sender` only from 3.1.3-23 (May 2025), and does not list it in `--help`, which is why Studio probes for it; a build between those is refused for pulls. Without the fix, Studio copy carries pulls and the plan says so. A send to a host and a copy within this computer do not depend on it.

macOS's openrsync has `--files-from`, `-0`, `--chmod`, `--max-size` and `--timeout`, but not `-s`, `--info`, `--no-inc-recursive` or `--trust-sender`: on a Mac it copies within the computer and sends to hosts, and pulls use Studio copy unless a newer rsync is installed. OpenBSD's openrsync has no `--files-from` and is not used.

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
| `POST /api/transfers/settings/test` | `{ ok, why?, rsync?, rsyncVersion?, rsyncText?, writable?, freeBytes?, roundTripMs?, sftpOnly?, rclone?, rcloneVersion? }` |

Errors are `{ "error": "..." }` with the status. With sign-in on every route acts for the signed-in user only, and another user's transfer is a 404.

## Settings

Stored under the `transfers` key of the settings file:

```
{ savedLocations: [{ id, label, host, root, rsyncPath?, port?, sshOptions? }],
  computerFolders: [{ path, label, builtIn, enabled }],
  movers: { rsync: { enabled, path }, studioCopy: { enabled: true },
            conduit: { enabled, server, mounts: [{ location, path, conduit }] },
            rclone: { enabled, path } },
  defaults: { mover, checksum, preserveTimes, parallel },
  hostLocations: { enabled, hidden: [host] } }
```

GET adds `managed`, `mcpServers` and `detected: { rsyncLocal, rsyncVersion?, rsyncPath?, rsyncText?, clusters, libraryHosts, rclone: { enabled, found, path?, version?, remotes: [{ name, type }], why? } }`. `rsyncText` says which transfers the rsync found is used for, for example `rsync 2.6.9 at /usr/bin/rsync: used on this computer and for sends to hosts; pulls use Studio copy (needs rsync 3.2.5 or a build with the CVE-2022-29154 fix)`; the test route's `rsyncText` says the same of a host's rsync. A PUT is validated field by field and a 400 names the field (`savedLocations[0].root: ...`). With sign-in on the section is `managed`, PUT and the test route are 403, and the values come from the environment: `STUDIO_TRANSFER_FOLDERS`, `STUDIO_RSYNC` (a path, or `off`), `STUDIO_TRANSFER_MOVER` and `STUDIO_TRANSFER_STREAMS`. rclone is off with sign-in on.

The local rsync is the configured name on `PATH`, then `/usr/bin`, `/bin`, `/usr/local/bin`, `/opt/homebrew/bin` and `/opt/local/bin`, so a desktop launch with a short `PATH` still finds it. On macOS with the default setting (`rsync`), Homebrew's `/opt/homebrew/bin/rsync` and `/usr/local/bin/rsync` are tried as well as the one on `PATH`, and the most capable is used: one that can pull, then one with protect args, then the newer. The desktop app takes the login shell's `PATH`, which finds Homebrew's rsync only when that `PATH` puts Homebrew ahead of `/usr/bin`, so this lookup does not depend on it. A path set in Settings is always used as it is. `ssh` and `pw` are found as the remote libraries find them.

## For the chat

`transfers.Of(app)` is the service the chat package calls: `Locations`, `Browse`, `Plan`, `Start(ctx, viewer, planID, startedBy, conversationID)`, `Hold`, `Approve`, `Deny`, `Get`, `List`, `Summary`, `Cancel` and `OnEnd`. `Start` returns once the job is queued; the job runs apart from the request. `Hold` records an `awaiting-approval` transfer, `Approve` re-checks its plan and queues it, `Deny` cancels it. `OnEnd` callbacks receive each transfer when it ends, with its `conversationId`.

The chat's `transfers` tool group (`server-go/internal/chat/transfertools.go`) has `transfer_locations`, `browse_location`, `transfer_status` and the two that change something, `transfer_files` and `cancel_transfer`, which are held for approval. `transfer_files` plans first: a plan that cannot run goes back to the model with its reason and nothing is held. A valid plan is held with `Hold`, so the Transfers list shows it as awaiting approval, and the approval card carries the plan. **Yes** calls `Approve` and the tool returns at once with a link (`#view=transfers?transfer=<id>`); **No**, an expired hold, or a call id reused for another call calls `Deny`. When an approved transfer ends, one note is added to its conversation. A note that arrives while a reply streams there waits until the reply is stored. The chat page reads the open conversation again when one of its transfers leaves the active list, and a question sent before that is placed after the note, on the same branch.

## Tests

`go test ./internal/transfers/` covers paths, plans, staging and verification (including movers that lie), restart, per-user isolation, settings, history at 10,000 records, CONDUIT against a fake MCP server, and end to end over HTTP. `STUDIO_TEST_SSHD=1 STUDIO_TEST_SSHD_PORTS=<first>-<last>` adds a run over three real sshd processes on loopback, one of them SFTP only. The SFTP path is also tested without ssh, against `github.com/pkg/sftp`'s server over in-process pipes. rclone is tested against a fake rclone (the test binary run again, `rclone_fake_test.go`) that keeps each remote in a folder, one of them behaving as an object store, and prints rclone's listings, features and JSON stats.
