# Libraries

A library is an index the Studio has mounted. The knowledge base the server
builds and owns is the first library; it is the only one the Studio writes
to, and every request that does not name a library means it, so nothing
that existed before libraries has changed. Any other library is read only:
a site's index built by root on a schedule, an index someone handed over
on a drive, or an index the Studio builds itself of a folder on this
computer or on a remote system.

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
Studio's enrichment tables, which are written into the primary index and
into the indexes the Studio builds (below); on any other library those
modes are skipped and search still answers from names and metadata. Upload, move, rename, delete, labels on
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

*In Settings, Libraries:* see the next section. Libraries added there are
kept in `libraries.json` under the index base, or, for those over ssh or
built on a cluster, in the user's own list (below).

## The Libraries page

Settings, Libraries is one list of every library the Studio searches, and
one **Add a library** control. Each row gives the library's name, its kind
in plain words with what the Studio may do with it (read and write, or
read only), where it is (the index directory, and the files when the
Studio can open them), its state, its depth, and its actions. For a library
the Studio builds (the main folder, and one built here or on a remote
system) the Depth column is its index depth, Names, Text, Text + OCR,
Meaning on documents or Meaning on all files, changed in place (`PUT
/api/libraries/:id/depth`); a built library starts at Text + OCR. For an
index the Studio only reads, it says what the index has, as built: names,
text, or text and meaning:

| Kind | Where it comes from | Actions |
|---|---|---|
| Main folder, read and write | `KB_ROOT` | **Change folder**, **Build index** or **Update index** (**Stop indexing** while it runs), and under the row **Delete index** and **View log**, on a single-user install; **View log** anywhere |
| Index of a folder on this machine, kept in step with it | `POST /api/libraries/build` | **Rebuild**, **Remove** |
| GUFI index on this machine | `POST /api/libraries`, or `STUDIO_LIBRARIES` (set by the deployment) | **Remove**, except for one the deployment set |
| Index built on a remote system over ssh or pw ssh | `POST /api/user-libraries/ssh/build`, `POST /api/user-libraries/pw/build` | **Rebuild**, **Remove** |
| GUFI index over ssh | `POST /api/user-libraries/ssh` | **Check**, **Remove** |
| GUFI index on a remote system over pw ssh | `POST /api/user-libraries/pw` | **Check**, **Remove** |
| Index built on a cluster by the platform's workflow | `POST /api/user-libraries` | **Refresh**, **Remove** |

**Add a library** (`web/src/components/AddLibrary.tsx`) asks two things.
*Where:* this computer, or a remote system. *How:* build an index of a
folder, or connect a GUFI index that already exists. A remote system is
reached with **Connect with** SSH or ACTIVATE (pw ssh), and on a Studio
the platform hosts the platform's index workflow is a third way to build.
Only what this install supports is offered: building here and anything
over ssh where sign-in is off (`canBuild` in `GET /api/libraries`, `ssh`
in `GET /api/user-libraries`); pw ssh where the pw CLI is on this machine
with a key to act with (`pw`), and on a Studio with sign-in off always
(`pwOffered`), where the form says why it cannot be used yet (`pwWhy`:
the Studio does not find the pw CLI, or the CLI is not signed in) with a
way to Settings, Model access; the workflow where the platform hosts the
Studio, which is sign-in on or `PW_PLATFORM_HOST` set (`workflow`), and
the client config's `platform`. A choice with one option is not shown. A
Studio on someone's own computer therefore never shows workflow or
scheduler fields: it builds on a remote system over the session instead.
A build shows its phase and the folders indexed so far under its row
while it runs, and the library joins the Library switcher when it is
ready. A rebuild of a library on this machine that already has an index,
including a build a restart interrupted (it runs again when the server
starts), writes the new index beside the old one and reads its text there
before swapping it in, so the library stays in search and Query, words
included, until the new index is complete. The ssh form (`SshLibraryForm`, `transport="pw"` for pw ssh, `mode`
connect or build) and the form for this machine (`LocalLibraryForm`,
`mode` connect or build) are the ones the setup wizard uses; their
default mode is connect.

For an index on this machine the form takes the index directory (the one
with `db.db`), the source directory if the files are on this machine, a
label, and an identifier. The identifier is optional: without one the
server makes it from the label, else from the index directory's name,
lowercased, and adds `-2`, `-3` to keep it unique. On a single-user
install either directory can also be picked from this computer's folders
(**Browse**, through `GET /api/setup/browse`). `kb` and `all` are never
used, since a request already means the main folder and every library by
them. The paths appear only on this page (`GET /api/libraries`); the
client config every viewer loads lists libraries without them. The Studio
probes the tree and refuses a path that is not a GUFI index, with the
reason.

On a single-user install the main folder's row also shows whether its
index is built, its file count (`GET /api/setup/status?files=1`, asked
after the page has loaded since it walks the index), and two actions.
**Change folder** opens the setup wizard's folder picker in a dialog and
saves the choice through `POST /api/setup/primary-folder`; where the app's
launcher runs the Studio it restarts on the new folder and the page waits
for it, and elsewhere the page says to set `KB_ROOT` and restart.
**Build index** (**Update index** once there is one) runs
`POST /api/index/build`, and the job's phases, each with its count and
time, show under the row as it runs, read from `GET /api/index/status`
like the footer and Stats; **Stop indexing** stops it, and the row then
says the index is incomplete until it is updated. Under the row,
**Delete index** asks first, in the row itself, and deletes the index and
its caches, never the files (`DELETE /api/index?confirm=1`); **View log**
shows the index log. Where users sign in, only **View log** is offered and
the state comes from the probe. See [REFERENCE.md](REFERENCE.md#indexing)
for the jobs and their phases.

## Libraries built from a folder on this computer

On a Studio with sign-in off, **Add a library**, "This computer", "Build
an index of a folder" indexes a folder here as the main folder is
indexed (`server/src/libraryBuild.ts`): `gufi_dir2index -x`, then text
extraction (`preextract` and `indexer/enrich.py`), then embeddings
(`indexer/embed.py`) where the model is installed, so full-text and
semantic search work there, as far as its index depth says (`depth`,
below the folder on the form: Text + OCR to begin with; at Names only
`gufi_dir2index` runs). It takes a source folder, an index folder
(optional), a label, an identifier, and the depth.

**Check** (`POST /api/libraries/build/check`) reports four steps: GUFI
is in `GUFI_BIN`; the source folder is readable, or does not exist yet
and can be made; the index folder is new, empty, or holds an index the
Studio built of this folder (`.studio-index.json`), is outside the
source folder, and is not inside the main folder's index; and what text
extraction covers. A source folder that is the main folder, holds the
Studio's own data folder, or is inside it, is refused. Without an index
folder the index goes in the Studio's own, `INDEX_BASE/libraries/<id>/index`.
**Build the index** (`POST /api/libraries/build`) asks before making a
folder that does not exist, and the server makes one only when the
request carries `create: true`; without it the answer is 409 naming the
folders. The build runs as an index job (`GET /api/index/job/<id>`); its
phase and folder count also show on the library's row
(`GET /api/libraries`, `build.progress`). The tree is written beside the
old one (`<index folder>/.build`) and swapped in when complete, so search
keeps answering during a rebuild. The index folder holds the tree,
`<index folder>/<name of the source folder>`, and `.studio-index.json`;
the extracted text goes in `INDEX_BASE/libraries/<id>/extract`, so a
file of this library never shows the main folder's text for the same
path.

The library is kept in step with its folder on the sweep interval
(Settings, General; 0 pauses it): folders whose content changed since the
last pass are indexed again, the topmost of each changed branch, with
extraction and embeddings for those alone, and the index of a folder that
is gone is removed. Files added to the folder by any means are picked up
this way. The pass reads the time of every folder and file on a worker
thread (`server/src/dirScan.ts`), so the server goes on answering while
it runs. A build that a restart interrupted runs again at startup.
**Rebuild** (`POST /api/libraries/<id>/rebuild`) indexes the whole folder
again. **Remove** forgets the library and removes
`INDEX_BASE/libraries/<id>`; an index folder that was chosen for it stays
where it is, and the source folder is never touched.

These libraries are read only in the Studio for now: no uploads, new
folders, renames, deletes, or labels from the Studio. Writing to them
would need the write routes, the extraction and preview caches, the label
overlay, and the incremental indexer, which all assume the main folder
today, to take a library's own folder, index, and caches; that is one
change across the indexing code, left for when it can be made in one
piece.

## Indexes built on a remote system

**Add a library**, "A remote system", "Build an index of a folder" builds
the index on the remote system itself, over the same session the Studio
uses to read it: plain ssh (`user@host` or `user@host:port`, with your
keys, agent, and `~/.ssh/config`, on a Studio with sign-in off) or
`pw ssh` to one of the systems the signed-in pw CLI reaches, picked from
a list (`GET /api/user-libraries/pw/systems`, below). No workflow and no scheduler are
involved (`server/src/remoteBuild.ts`).

**Test connection** (`POST /api/user-libraries/ssh/build/test`, or
`.../pw/build/test`) reports five steps: the session reaches the system;
GUFI is installed there, both `gufi_dir2index` and `gufi_query`, looked
for where the connection test looks for `gufi_query`, and if not, a plain
message saying so and where to get it; the source folder is readable, or
does not exist yet and can be made; the index folder is new, empty, or
holds an index the Studio built of this folder, outside the source
folder; and whether text extraction can run there, which needs Python
3.10 or later with SQLite's full-text module and only adds a note when it
cannot. Without an index folder the index goes in
`~/.activate-studio/indexes/<name>-<hash>` there, as the index workflow
does, and that folder needs no confirmation. Paths are absolute or start
with `~`, which is resolved there. **Build the index**
(`POST /api/user-libraries/ssh/build` or `.../pw/build`, with
`{label?, destination | cluster, sourceRoot, indexRoot?, gufiBin?, depth?, create?}`)
asks before making a folder the user named, and the server tests again
first.

The build runs on the remote system as a process of its own, detached
from the session (`setsid` where there is one, `nohup` always), so it
goes on when the session or the Studio stops. Its state is a folder there,
`~/.activate-studio/builds/<build id>`: the script, its phase, process id,
exit status, error, and logs. It runs `gufi_dir2index -x` into
`<index folder>/.build` and swaps the tree in when complete; where Python
is there and the depth reads text, the Studio copies `enrich.py` into the
state folder and runs it (with `--no-ocr` at Text), keeping extracted
text in `<index folder>/.extract`, and at the meaning stops, where a GUFI
with the embedding extension and `~/.activate-studio/models/minilm384.gguf`
are there too, `embed.py`, which uses that GGUF model through sqlite-lembed (the ONNX backend runs only on this computer). It then counts the index and writes
`.studio-index.json`, with `fullText` true only when that system's
`gufi_query` can read the text tables. The Studio follows the build as a
background job (`GET /api/jobs/<id>`), reading its state every few
seconds over the session and keeping the phase and folder count on the
library; after a restart the next listing follows it again, and an
update does not wait for it. When it ends the library is ready with its
counts, or says why not; a rebuild that fails leaves the index it had.
**Rebuild** runs the build again beside the current index. **Remove**
forgets the library and leaves the index there. Search on these
libraries covers names, metadata, and, with extraction, full text;
results by meaning are not read from a remote index yet.

## The Details card

On the Library page the status panel's first card, **Details**, describes
what was last clicked in the tree, a folder or a file, or with nothing
clicked the library itself (`web/src/components/LibraryDetails.tsx`).
It reads `GET /api/kb/details?path=<path>&library=<id>`
(`server/src/libraryDetails.ts`), answered from the library's index
wherever it is, on this computer or over the session:

| Field | From |
|---|---|
| `size`, `files`, `dirs` (below the folder), `mtime` (the latest change below it) | one aggregate `gufi_query` over the folder's subtree (`treeStats` in `gufi.ts`) |
| `types`, the extensions taking the most space | the same query |
| `labels.within`, labels applied below the folder, with counts | a second aggregate over the xattrs tables; for the main folder, the label maps search uses, overlay included |
| `labels.own`, `labels.inherited` | for the main folder, the labels search uses; elsewhere the entry's own xattr |
| a file's `size`, `mtime`, and labels | its entry in its folder's index, or the file itself before it is indexed |
| `library`, `index` | the library and, on a single-user Studio or for the viewer's own remote libraries, where its index is |

A library on another system is not counted as a whole when it is
selected, since on a large index that walk takes minutes on the remote
system: the card has `counted: false`, the files and folders from its
connection test or build, and a **Count size and types** button, which
asks again with `count=1`. Its folders are counted when clicked.

Counts are kept until the index changes: for a library on another system,
until its recorded index time does, or an hour; for one here, until an
index job ends, or ten minutes. Two requests for the same counts while
they run share one query. The whole answer is also kept 20 seconds on the
server and 30 in the page, and is dropped when the library changes. With All
libraries the path starts with the library's id, and nothing is asked
until something is clicked.

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

## Indexes built on a cluster

On a Studio the platform hosts, a user can also build through the
platform's index workflow: Settings, Libraries, **Add a library**, "A
remote system", "Build an index of a folder", **Connect with** "Platform
workflow": a cluster from their active clusters, a directory
(`$WORKDIR` by default), a label, and optionally a Slurm partition and
account. A Studio on someone's own computer does not offer this; it
builds on the remote system over ssh or pw ssh instead (above). The
Studio then:

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

The commands run over long-lived `pw ssh` sessions, up to three per user
and cluster (`server/src/remoteExec.ts`; `STUDIO_REMOTE_SESSIONS`, 1 to
8): a request takes an idle session, else opens another, else waits on
the least busy one, so a long count does not hold up a search. Each
request is the command's argv with every argument base64-encoded, run on
the cluster without a shell, so SQL and paths never meet shell quoting.
The first request pays for the ssh setup, about 5 seconds; later ones
take about 0.3 seconds. A session closes after 10 idle minutes.

A `gufi_query` on a remote system, and a build there, runs with that
system's processor count, up to 16 threads: `nproc`, which counts only
the processors the login may use, else `getconf _NPROCESSORS_ONLN`, else
`sysctl -n hw.ncpu`, asked once per session host and 4 when none answers.
`STUDIO_REMOTE_THREADS` (1 to 256; in the macOS app, a line in
`studio.env`) sets one count for every remote system, for a login node
whose policy allows fewer.

To the rest of the Studio a user library's index root is an opaque
reference (`remote:<owner>:<id>`); `server/src/gufi.ts` sends any query on
one to the cluster, so search and statistics work unchanged. Definitions
live in `INDEX_BASE/users/<owner hash>/libraries.json`. Routes:
`GET /api/user-libraries`, `POST /api/user-libraries`,
`POST /api/user-libraries/<id>/refresh`, `DELETE /api/user-libraries/<id>`.

## Libraries over ssh

A library can also be an existing GUFI index on another machine that the
Studio's host reaches with plain ssh: Settings, Libraries, **Add a
library**, "A remote system", "Connect an existing index", **Connect
with** ssh, or the setup wizard. The host is written `user@host`, an
alias from `~/.ssh/config`, or either with `:port`. The Studio runs
`ssh -T -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -o ControlMaster=auto -o ControlPath="<folder>/<hash>" -o ControlPersist=600 [-p port] <destination>`
with the keys, agent, and `~/.ssh/config` of the account it runs as, and
keeps up to three sessions per destination, as it does per cluster. The
sessions to one host share a single ssh connection through a socket,
which stays open ten minutes after the last session closes, so a new
session does not log in again; `~/.ssh/config` is not changed. The
socket's name is 12 hex characters of the destination and port, and its
folder is the first of these that will do:
`$XDG_RUNTIME_DIR/activate-studio` where that variable is set,
`/tmp/activate-studio-<uid>`, and `INDEX_BASE/ssh`. A folder is made with
mode 700 when missing and checked with `lstat` before each session: it has
to be a directory, not a link, owned by the user, with mode 700, so one
that another user made first in `/tmp` is passed over. It also has to be
short enough for the socket: ssh binds the socket at its path plus 17
characters and macOS allows 104 bytes, so the whole path stays within 86.
The `/tmp` folder fits every user name; the macOS data folder under
`~/Library/Application Support` is too long for names over about 12
characters. With no folder that will do, or with
`STUDIO_SSH_SHARE=0`, each session logs in on its own. A socket left by a
master that was killed is removed by the next ssh, which then logs in and
becomes the master. Nothing is built or written on that machine: the
library is ready once its connection test passes, and it is read only.

The remote machine needs:

- key access with no prompt (BatchMode refuses password and passphrase
  prompts, so load the key into your agent), and its host key in
  `known_hosts`: connect once from a terminal and accept it;
- bash, and GUFI's `gufi_query`. The test looks on the PATH of a
  non-interactive login, then in `~/.activate-studio/gufi/*/bin` (where
  the index workflow installs GUFI, newest first), `/usr/local/bin`,
  `~/usr/local/bin`, and `/opt/gufi/bin`; anywhere else goes in the GUFI
  bin directory field. On
  macOS, GUFI's tools are started with `DYLD_LIBRARY_PATH=<prefix>/lib`,
  `<prefix>` being the directory above the bin directory;
- an index built there with `gufi_dir2index`. The index directory is the
  one with `db.db`, which `gufi_dir2index` writes one level below its
  output directory, named after the indexed directory.

A source directory is optional. With it, text files open as previews read
over ssh; without it, the library is browsed and searched by name and
metadata. Paths may start with `~`, which is resolved on the remote
machine.

The connection test reports four steps, each with what to change when it
fails: ssh reaches the host; `gufi_query` is found and runs, with its
version; the index directory has a `db.db`, counted with one aggregate
`gufi_query`; the source directory is readable. Adding takes the result
of a test of the same index that passed in the last two minutes, and runs
the test again otherwise. **Check** reruns it and updates the counts and the index
time (the modified time of the index's `db.db`), which is also when
Statistics counts the library again.

The ssh identity is the server account's, so libraries over ssh are
offered only on a Studio with sign-in off, which has one user; with
sign-in on, the routes answer 403. Routes:
`POST /api/user-libraries/ssh/test` and `POST /api/user-libraries/ssh`,
both with `{label?, destination, port?, indexRoot, gufiBin?, sourceRoot?}`;
listing, refresh, and removal use the routes above, and list entries
carry `transport` (`pw` or `ssh`), `destination`, and `port`.

## Libraries over pw ssh

An existing GUFI index on one of the user's running clusters is connected
the same way, with `pw ssh <cluster>` in place of ssh: Settings,
Libraries, **Add a library**, "A remote system", "Connect an existing
index", **Connect with** pw ssh, or the setup wizard's Other libraries
step. The system is picked from the user's systems on the platform
(`GET /api/user-libraries/pw/systems`, which runs `pw clusters ls` with
the same key): running ones first, one that is off marked so (it has to
be started on ACTIVATE before it can be reached), the display name over
the name `pw ssh` takes, and "Another system…" to type a name. The route
answers `{ usable, why, systems, error }`: `why` when pw ssh cannot be
used yet, and `error` when the list could not be had, in which case the
form shows it and takes a typed name. `pw ssh` acts with the owner's platform key; with
sign-in off that is the deployment credential, which on a computer of its
own is the pw CLI's sign-in there. Nothing is built or written on the cluster: the
library is ready once its connection test passes, and it is read only.
An index the workflow built for another Studio can be connected this way.

The test has the four steps of the ssh test, and the same messages:
`pw ssh` reaches the cluster; `gufi_query` is found and runs; the index
directory has a `db.db`, counted with one aggregate `gufi_query`; the
source directory, when given, is readable. A failed first step says what
it means: the pw CLI is not on this machine, its sign-in is missing or
has expired (run `pw auth`), or the cluster is not one of the user's
running clusters. **Check** reruns the test and updates the counts; it
never starts the index workflow.

Routes: `POST /api/user-libraries/pw/test` and `POST /api/user-libraries/pw`,
both with `{label?, cluster, indexRoot, gufiBin?, sourceRoot?}`. The
cluster name starts with a letter or digit, so `pw ssh` cannot read it as
an option, and the paths follow the ssh rules (absolute, or under `~`).
Without a key to act with they answer 409. `GET /api/user-libraries`
carries `pw`: whether the pw CLI is found (as `PUT /api/access/cli/path` in REFERENCE.md describes)
and there is a key, and `workflow`: whether the index workflow is
offered. List entries carry `transport: "pw"` and `existing: true`,
which a connected library over ssh carries too; a library built on a
cluster or on a remote system has `existing: false`, and one the Studio
built over the session also `built: true`, `indexDir`, `progress` while
it runs, and `fullText` and `vectors`.

## One library or all

The Library switcher lists every library the viewer can read and an entry
for all of them, and the Library tree, Search, the Query page, Stats, and
the index health popup follow it. All is the default where there is more
than one library. Each browser tab keeps its own choice in session
storage, so a reload keeps it and a new tab starts from the default. Every route that reads a library takes
`library=<id>`, and these take `library=all` too:

- the tree, which then has one top-level folder per library, named by its
  id, with that library's own tree below; a path in it is
  `<library id>/<path>`, and files open and download from their library;
- search, which runs in each library what that library supports (full
  text and semantic where its index has them, names always) and blends the
  hits, each tagged with its library. Each library has 20 seconds to
  answer (`STUDIO_SEARCH_DEADLINE_MS`); one that has not is left out of
  the results and named in the answer's `slow`, and the Search page and
  the chat's `search_kb` say so. A library searched on its own is waited
  for. With `gufi_vt` as the engine, its one statement waits for every
  library;
- the Query page, where the rows gain a leading library column. For the
  canned queries and the builder, whose order the Studio knows, the rows
  are the overall top rows and grouped totals (by extension) are added up;
  raw SQL rows are listed library by library. A library that cannot answer
  is named beside the rows;
- statistics, as a total with a line per library.

With All selected, every other request on a path goes to that path's
library, with the library's folder taken off the path and put back on the
paths it returns (`withLibrary` and `withLib` in `web/src/api.ts`). Files
of every type open, and the writable library stays writable inside its
folder: uploads, new folders, renames, moves, deletes, and labels. A
library's own folder takes uploads and new folders and is not renamed or
deleted, and a request whose paths span two libraries is refused. Labels
belong to the knowledge base, so a label filter on a search over all
libraries keeps knowledge-base hits.

A library on a cluster takes seconds to count, so Stats clears its figures
and shows a loader while a library loads, and the server keeps a cluster
library's counts until the library is next built, and a library here
until an index job ends.

The chat has its own library control, beside Persona, and the assistant's
tools take a `library` argument: `search_kb` and `query_corpus` accept
`all`, `read_kb_file` and `list_kb_dir` take one library's id, and
`list_libraries` says which ids exist. A cluster library's files are read
and listed on the cluster.

## One query over several libraries

Where the Studio host's GUFI has gufi_vt, a query over several libraries
runs as one gufi_vt statement: a virtual table per library, unioned, with
the overall order and limit in the outer SELECT. A library on this host is
a local location. A library on a cluster is a remote location whose
command is the Studio's wrapper (`gufiVtRemote.js`): gufi_vt hands it the
gufi_query command line, the wrapper splits it without a shell and passes
it back to the server over loopback, and the server runs gufi_query on the
cluster through the user's session. No shell parses the SQL on either
side, and the user's key never appears in a process's arguments. The
server accepts only gufi_query with the stages gufi_vt sends, on that
location's own index, under a per-process secret and a per-query token.

gufi_vt fails the whole statement when one location exits nonzero with no
rows, so each location is checked first (the index exists, and on a
cluster GUFI is installed) and one that fails is left out and named.
gufi_vt runs the locations one after another. Without gufi_vt on the host,
for a plan whose SQL contains a double quote (gufi_vt's own delimiter), or
when the statement fails, each library answers on its own and the server
merges the rows. The response says which engine answered.

## The GUFI commands

Search, the Query page, and the chat's `search_kb` and `query_corpus`
tool blocks show the GUFI commands behind their results, as a shell takes
them. Name and full-text search run as one aggregate `gufi_query` (`-a 1` with
`-I`, `-E`, `-J`, `-K` and `-G`): each thread keeps rows until it has the
number asked for, and the merge returns that many, so a common word in a
large index sends back the rows shown and not every folder's matches.
A command on this host is shown as run. A cluster's command is shown
under the cluster's name, to run there after `pw ssh <cluster>`. A gufi_vt
statement is shown as a here-document with gufi_vt's usual remote form,
`remote_cmd='pw', remote_arg='ssh', remote_arg='<cluster>'`, with the
cluster's GUFI added to the PATH there, so it runs from a terminal that
has GUFI and the pw CLI. The chat model does not receive the commands.

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

Writing to a library built from a folder on this computer (uploads, new
folders, renames, deletes, labels), which needs the indexing code to take
a library's own folder, index, and caches. Results by meaning from an
index on a remote system. Enrichment for connected read-only libraries,
in sidecar databases beside the index on GUFI's external attach
mechanism, so full-text and semantic search work there too. Search across libraries as one gufi_vt statement; it runs
library by library. Refreshing a
user's library on a schedule, which needs the user's key at that time:
personal keys live in memory and the browser's sealed cookie, so until
delegated sign-in through the platform's OIDC applications arrives, a
refresh runs when the user asks for it.
