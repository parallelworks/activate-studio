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

*By an administrator:* Settings > Libraries lists what is mounted and what
each supports, and adds one by path. The Studio probes the tree and
refuses a path that is not a GUFI index, with the reason. Added libraries
are kept in `libraries.json` under the index base.

## Sections

`STUDIO_SECTIONS` chooses which parts of the app appear, from `chat`,
`library`, `search`, `query`, `overview`, `history`, and `agents`. Leave it
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
across several libraries in one query. A remote library reached through
GUFI's client rather than a local tree. Each is a follow-on to this
mechanism rather than a change to it.
