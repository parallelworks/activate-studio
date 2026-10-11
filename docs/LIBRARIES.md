# Libraries

A library is an index the Studio has mounted. The knowledge base the server
builds and owns is the first library; it is the only one the Studio writes
to, and every request that does not name a library means it, so nothing
that existed before libraries has changed. Any other library is read only:
a site's index built by root on a schedule, or an index someone handed
over on a drive.

## What a library carries

| Field | Decides |
|---|---|
| `id`, `label` | what the user picks in the Library switcher, and which library a path belongs to |
| `indexRoot` | the GUFI tree: a directory with a `db.db` at its root |
| `sourceRoot`, optional | whether files can be opened, or only searched and described |
| writable | whether the Studio may add files, index, and enrich; the primary only |

When a library is added the Studio probes it once and records what it
found: whether it is a GUFI index, whether the Studio's full-text tables
are present, whether vector tables are present, and whether the source is
readable. Nothing is asked of GUFI and nothing is written into the index.

## What works on a read-only library

Browsing, filename and metadata search, statistics, and opening files
when a source root is set. Full-text and semantic search need the
Studio's enrichment tables, which today are written only into the primary
index; on any other library those modes are skipped and search still
answers from names and metadata. Upload, move, rename, delete, labels on
files, and re-indexing are refused with a 403 that says the library is
read only, and the interface does not offer them.

A library without a source root can be searched and described but not
opened: the file routes answer 409, and the viewer says the file is not
reachable from this host. That is the normal shape for an index of a
filesystem the Studio's machine cannot see.

## Adding libraries

*By the deployment:* the ACTIVATE deploy form has an Additional Libraries
field under Knowledge Base, a JSON list, and a Visible Sections field. The
same two settings are the environment variables `STUDIO_LIBRARIES` and
`STUDIO_SECTIONS` when running standalone:

```
STUDIO_LIBRARIES='[{"id":"scratch","label":"Scratch","indexRoot":"/gufi/scratch","sourceRoot":"/lustre/scratch"}]'
STUDIO_SECTIONS='library,search,overview'
```

Libraries set this way are pinned: they appear for every user and cannot
be removed from Settings.

*By an administrator:* Settings > Libraries lists what is mounted, with
each library's index root and source root, and what each supports, and
adds one by path. The paths appear only there (`GET /api/libraries`); the
client config every viewer loads lists libraries without them. The Studio probes the tree and
refuses a path that is not a GUFI index, with the reason. Added libraries
are kept in `libraries.json` under the index base.

## Indexes on a cluster

`deploy/gufi-index.yaml` is an ACTIVATE workflow that builds or updates a
GUFI index of a directory on a cluster, as the user who runs it. It
installs GUFI's core tools from source into the user's account once per
commit, then runs the index through the platform's `script_submitter`
subworkflow, on the login node or as a Slurm or PBS job. The Studio runs
it for a user's own libraries (below); it can also be added as a platform
workflow and run by hand.

| Input | Default |
|---|---|
| `resource` | the cluster |
| `cluster.scheduler`, `cluster.slurm`, `cluster.pbs` | run on the login node |
| `index.source_dir` | `$WORKDIR`, or the home directory where it is not set |
| `index.index_dir` | `~/.activate-studio/indexes/<name>-<hash>` |
| `index.mode` | `update`: rescan directories changed since the last run, or build if there is no index; `build` rebuilds |
| `index.threads` | 4 |
| `gufi.prefix` | `~/.activate-studio/gufi` |
| `gufi.source` | GUFI's GitHub repository at a pinned commit, or a `pw://` tarball of the source |

The directory being indexed is only read. The index directory must be
outside it, and a rebuild is swapped in when complete. Each run writes
`.studio-index.json` in the index directory, with the index path, the
GUFI binaries, the build time, and directory and file counts, and prints
it in the run log between `STUDIO_INDEX_BEGIN` and `STUDIO_INDEX_END`. An
update runs from the directory that holds the indexed one, because GUFI
creates a temporary file there; where that directory is not writable, the
run rebuilds instead. An index takes about 50 KB per directory.

## Your libraries

A user adds a directory on one of their clusters under Settings, Libraries,
"Your libraries": a cluster from their active clusters, a directory
(`$WORKDIR` by default), a label, and optionally a Slurm partition and
account. The Studio then:

- registers the index workflow in the user's account the first time, named
  `studio-gufi-index-<hash>` after the workflow's content, so a changed
  workflow registers afresh;
- runs it with the user's own platform key, the one their chat uses (on a
  Studio with sign-in off, the deployment's), as that user on the cluster;
- follows the run while Settings is open, and when it completes reads the
  index summary from the run log, resolves the index path and the GUFI
  binaries on the cluster, and marks the library ready.

A ready library joins that user's Library switcher, labeled with its
cluster, and nobody else's. It is read only. The tree lists each folder
from the index on the cluster (one `gufi_query` per level), name search
runs there, and text files open as a preview read from the cluster.
Full-text and semantic search need enrichment, which these libraries do not
have yet. **Refresh** reruns the workflow in update mode; **Remove** drops
the library from the Studio and leaves the index on the cluster.

The commands run over one long-lived `pw ssh` session per user and cluster
(`server/src/remoteExec.ts`): each request is the command's argv with every
argument base64-encoded, run on the cluster without a shell, so SQL and
paths never meet shell quoting. The first request pays for the ssh setup,
about 5 seconds; later ones take about 0.3 seconds. The session closes
after 10 idle minutes.

To the rest of the Studio a user library's index root is an opaque
reference (`remote:<owner>:<id>`); `server/src/gufi.ts` sends any query on
one to the cluster, so search and statistics work unchanged. Definitions
live in `INDEX_BASE/users/<owner hash>/libraries.json`. Routes:
`GET /api/user-libraries`, `POST /api/user-libraries`,
`POST /api/user-libraries/<id>/refresh`, `DELETE /api/user-libraries/<id>`.

## Sections

`STUDIO_SECTIONS` chooses which parts of the app appear, from `chat`,
`library`, `search`, `query`, `overview`, `history`, `agents`, and
`workflows`. Leave it
unset for all of them. A site that wants an index viewer and nothing else
sets `library,search,overview`, and the assistant and the agents are not
rendered. Settings and Help always remain reachable.

## Addressing

Every request that touches the corpus or the index accepts a `library`
query or body parameter. The client adds it from the current selection,
so views never build it themselves; the primary is sent as nothing. Deep
links carry it as `&lib=<id>` after the path, so a shared `#open=` link
and a reload land in the right library.

## Not yet

Enrichment for read-only libraries, in sidecar databases beside the index
on GUFI's external attach mechanism, so full-text and semantic search work
there too. The Query page, which still scopes to the primary. Searching
across several libraries in one query, through gufi_vt. Refreshing a
user's library on a schedule, which needs the user's key at that time:
personal keys live in memory and the browser's sealed cookie, so until
delegated sign-in through the platform's OIDC applications arrives, a
refresh runs when the user asks for it. Each is a follow-on to this
mechanism rather than a change to it.
