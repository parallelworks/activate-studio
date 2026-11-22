# Libraries

A library is an index the Studio has mounted. The knowledge base the server
builds and owns is the first library; it is the only one the Studio writes
to, and every request that does not name a library means it, so nothing
that existed before libraries has changed. Any other library is read only
(a site's index built by root on a schedule, an index someone handed over
on a drive, or an index the Studio builds itself of a folder on this
computer), except those written as the person: My library, a folder
shared with them for writing, and an index they built on one of their
own systems ([Indexes built on a remote computer](#indexes-built-on-a-remote-computer)).

## What the libraries are called

The names follow whether people sign in, so each mode has one My library.

- Sign-in off (the Mac and Linux apps, a Studio one person uses): the main
  library is the person's own folder, read and written as them, and its
  default name is **My library** (`settings.MainLabelPersonal`). A folder of
  their own on a remote computer (Add a library, Remote computer, **A new folder**) is
  another library, named by its folder and shown with "(on <system>)". A
  folder made that way before this rule, stored under the name My library,
  keeps its record and files and is shown by its folder's name
  (`libraries.UserDef.ShownLabel`); nothing stored changes.
- Sign-in on: the main library is the one people share, read only for all
  but its administrators, and its default name is **Shared library**
  (`settings.MainLabelShared`). **My library** is each person's own folder
  on a remote computer (Add a library, Remote computer, **My own folder**).

A name set in Settings, General (`kbLabel`) or by `KB_LABEL` is used in
either mode. `/api/config` answers `signIn`, and the web app's
`libraryNames()` (`web/src/libraryNames.ts`) gives the names the pages use.

## What a library carries

| Field | Decides |
|---|---|
| `id`, `label` | what the user picks in the Library switcher, and which library a path belongs to |
| `indexRoot` | the GUFI tree: a directory with a `db.db` at its root |
| `sourceRoot`, optional | whether files can be opened, or only searched and described |
| writable | whether the Studio may add files, index, and enrich; the primary only |
| `access` | what the library is, in the terms Add a library uses (below) |

`access` is worked out from the library's record each time it is listed
(`server-go/internal/libraries/access.go`), so a library added before it,
and a library list exported from one, reads the same as one added now;
nothing is stored or migrated. It has four parts:

- `where`: `here` (this computer), `system` (a cluster or host, with
  `system` its name and `via` one of `pw`, `ssh` or `workflow`), or
  `bucket`.
- `readAs`: `you`, the viewer's own sign-in or ssh keys, for every library
  a person adds on one of their systems, or `studio`, the account the
  Studio runs as, for the libraries on this computer and in buckets, which
  on a shared Studio are the deployment's.
- `write`: whether the viewer may change its files from the Library. Only
  libraries read as the viewer can be: My library, a folder shared with
  them for writing, and an index they built on one of their systems.
- `index`: `built` by the Studio, or `connected` as it was, which is always
  read only, and `sharedFrom`, the account that shared a folder.

The switcher, the scope pickers and Settings name a library by these:
Read/write, Read only or Shared, then "(on" its system")".

When a library is added the Studio probes it once and records what it
found: whether it is a GUFI index, whether the Studio's full-text tables
are present, whether vector tables are present, and whether the source is
readable. Nothing is asked of GUFI and nothing is written into the index.

The probe also reads the columns of the index's top summary table, which say which GUFI wrote it (`format` in `library-caps.json`; see GUFI versions in [`REFERENCE.md`](REFERENCE.md#indexing)). An index written by an earlier GUFI is read as it was, and nothing is said of it, with one exception: an index from before GUFI 0.8.0's `rollupscore` rename that was rolled up is searched in part, named so in the omissions line and in this page (`formatNote`), since 0.8.0 lists the files of a rolled-up folder more than once. A library on another system gets the same answer from the `gufi_query` there, at its connection test.

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
filesystem the Studio's machine cannot see. The assistant can still read
the text the index holds for its files (`read_kb_file`), and a library
can name another host that has the files, which the viewer and the
assistant's `read_original` then try (see "Reading originals through
another host").

## Adding libraries

*By the deployment:* the ACTIVATE deploy form has an Additional Libraries
field under Knowledge Base, a JSON list, and a Visible Sections field. The
same two settings are the environment variables `STUDIO_LIBRARIES` and
`STUDIO_SECTIONS` when running standalone:

```
STUDIO_LIBRARIES='[{"id":"scratch","label":"Scratch","indexRoot":"/gufi/scratch","sourceRoot":"/lustre/scratch"}]'
STUDIO_SECTIONS='library,search,overview'
```

With sign-in on, the libraries on this computer are the deployment's:
they are read with the Studio's own account for everyone who signs in, so
nobody signed in adds or removes one from the Studio (`POST
/api/libraries` and `DELETE /api/libraries/{id}` answer 403, and Add a
library does not offer this computer). A person adds libraries of their
own on their remote computers instead, read as them.

Libraries set this way are pinned: they appear for every user and cannot
be removed from Settings.

*In Settings, Indexing, Libraries:* see the next section. Libraries added there are
kept in `libraries.json` under the index base, or, for those over ssh or
built on a cluster, in the user's own list (below).

*From a library list:* a file exported from another Studio, imported in
Settings, Indexing, Libraries, in Add a library, from the Library's Add
menu, or in setup (see "Library lists").

*From the Library page:* the arrow beside **+ Add** opens a menu of **Add
files…**, **Add a library…** and **Import a library list…**. The last two
open the Settings components (`AddLibrary`, `LibraryImport`) in dialogs
over the page (`web/src/components/LibraryAddDialogs.tsx`), so they work
with Settings out of view. The arrow is shown when the viewer may add a
library, from the same answers the forms use: `canBuild` in `GET
/api/libraries` (sign-in off) offers everything; with sign-in on, a
viewer who has a list of their own (`available`) and can use pw ssh
(`pwOffered`) or the platform's workflow (`workflow` with the client
config's `platform`). Otherwise **+ Add** is the button alone. An import
that adds every ticked library closes its dialog and reloads the client
config, so the libraries join the switcher; one with a failure stays open
on each entry's result.

## The Libraries page

Settings, Indexing, Libraries is one list of every library the Studio searches, and
one **Add a library** control. Each library is a card, laid out the same
for every kind. Its head gives the library's name with its access as the
Library switcher names it (Read/write, Read only or Shared, and the
computer it is on), its kind in plain words, its state with its counts and
when it was last indexed, and at the right the action that keeps its
index current (**Update index**, **Rebuild**, **List again**, **Check** or
**Refresh**, in the same place for every kind) with the kind's other
everyday actions. What removes it, **Delete index** for the main folder
and **Remove** for the others, is under **More**, and each asks first.
Under the head are its notes (errors, gaps, what a listing costs), then
its details: the folder and the index directory, each on one line,
shortened in the middle where the card is narrow, whole on hover and in
**Copy**, and the kind's settings. For a library
the Studio builds (the main folder, and one built here or on a remote
computer) its **Depth** is its index depth, Names, Text, Text + OCR,
Meaning on documents or Meaning on all files, changed in place (`PUT
/api/libraries/:id/depth`). A library built here or on a remote computer
starts at Meaning on documents (both below). A new depth on a library
built on a remote computer is stored and applies at its next build, so the
card offers **Rebuild now** or **Later**, or, with a build running, says the
change waits for the next one. The library records the depth of its last
build that finished with what that depth makes (`builtDepth`), and until
that matches its depth its notes say **Depth changed, Rebuild to
apply**. A failed rebuild changes nothing there, nor does one that made no
vectors at a Meaning depth; a library last built by an earlier version has
no record and shows no note.
The depth is stored with the library when it is added, so a later change
of the default leaves it as it was built, and a library added by an
earlier version, with no depth stored, keeps what it had: Text + OCR, or
what its settings said. For an index the Studio only reads, it says what
the index has, as built: names, text, or text and meaning:

| Kind | Where it comes from | Actions |
|---|---|---|
| Main folder, read and write | `KB_ROOT` | **Change folder**, **Build index** or **Update index** (**Stop indexing** while it runs), and **Delete index** under **More**, on a single-user install; **View log** at the card's foot anywhere |
| Index of a folder on this machine, kept in step with it | `POST /api/libraries/build` | **Rebuild**, **Remove** |
| Copy of a folder in a Transfers location, kept in step with it | `POST /api/libraries/copy` | **Update now**, how often it looks for changes, **Rebuild**, **Remove** |
| GUFI index on this machine | `POST /api/libraries` (sign-in off), or `STUDIO_LIBRARIES` (set by the deployment) | **Remove** (sign-in off), except for one the deployment set |
| Index built on a remote computer over ssh or pw ssh | `POST /api/user-libraries/ssh/build`, `POST /api/user-libraries/pw/build` | **Rebuild**, **Remove** |
| GUFI index over ssh | `POST /api/user-libraries/ssh` | **Check**, **Remove** |
| GUFI index on a remote computer over pw ssh | `POST /api/user-libraries/pw` | **Check**, **Remove** |
| Index built on a cluster by the platform's workflow | `POST /api/user-libraries` | **Refresh**, **Remove** |

A row of a library whose files may be on another host also has **Originals via**,
the host its originals are read through when its own location cannot give a
file (see "Reading originals through another host").

**Add a library** (`web/src/components/AddLibrary.tsx`) is one form, used
in Settings and in the Library's Add menu, in the terms of `access` above.
*Where:* this computer (sign-in off), a system, or a bucket. *System:* one
of the viewer's clusters over pw ssh (`PwSystemField`, the Studio's own
cluster first), or a host over plain ssh where sign-in is off; it is
picked once and the forms below take it. The form then says the library
is read as the viewer there. *What:* on a remote computer, the viewer's own folder
(My library), an index the Studio builds of a folder there, or an index
that is there already, connected read only; here, a folder to build or an
index to connect. A folder on a cluster can be built over the pw ssh
session or, on a Studio the platform hosts, by the platform's index
workflow (**Build with**). On a cluster, the folders others shared with
the viewer there are listed under the form (`SharedWithMe`, by the
share's `site`). A copy of a Transfers location is not here: it is made
from Transfers (Copies of a location, below), and the form points there.
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
scheduler fields: it builds on a remote computer over the session instead.
A build shows its phase and the folders indexed so far in its card
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

On a single-user install the main folder's card also shows whether its
index is built, its file count (`GET /api/setup/status?files=1`, asked
after the page has loaded since it walks the index), and two actions.
**Change folder** opens the setup wizard's folder picker in a dialog and
saves the choice through `POST /api/setup/primary-folder`; where the app's
launcher runs the Studio it restarts on the new folder and the page waits
for it, and elsewhere the page says to set `KB_ROOT` and restart.
**Build index** (**Update index** once there is one) runs
`POST /api/index/build`, and the job's phases, each with its count and
time, show in the card as it runs, read from `GET /api/index/status`
like the footer and Stats; **Stop indexing** stops it, and the card then
says the index is incomplete until it is updated. **More**, **Delete
index** asks first, in a dialog, and deletes the index and its caches,
never the files (`DELETE /api/index?confirm=1`). The card's foot says how
the last run went, with **View log** for the index log. Where users sign in, only **View log** is offered and
the state comes from the probe. See [REFERENCE.md](REFERENCE.md#indexing)
for the jobs and their phases.

## Library lists

A library list is a file of libraries that one Studio exports and another
imports, so an administrator can set up the same libraries for many
people in one step: each person imports the file into their own Studio,
and each library is added there as that person, with their own ssh login
and their own permissions (see [`ACCESS-CONTROL.md`](ACCESS-CONTROL.md)).
The file is JSON, saved as `<label>.studio-libraries.json`:

```json
{
  "format": "activate-studio-libraries",
  "version": 1,
  "exportedAt": "2026-11-10T12:00:00.000Z",
  "studioVersion": "v1.191",
  "libraries": [
    { "kind": "index", "label": "Shared drive", "indexRoot": "/mnt/gufi/shared",
      "readThrough": { "destination": "files.example.org", "root": "/export/shared" } },
    { "kind": "folder", "label": "Projects", "sourceRoot": "/srv/projects", "depth": "text" },
    { "kind": "ssh", "label": "Index server", "mode": "connect", "destination": "index.example.org",
      "port": 2222, "sshOptions": ["-J", "jump.example.org"], "indexRoot": "/gufi/shared",
      "sourceRoot": "/srv/shared", "gufiBin": "/opt/gufi/bin" },
    { "kind": "ssh", "label": "Data", "mode": "build", "destination": "box.example.org",
      "sourceRoot": "~/data", "depth": "meaning-documents" },
    { "kind": "pw", "label": "Cluster index", "mode": "connect", "cluster": "cluster-a",
      "indexRoot": "/scratch/gufi" }
  ]
}
```

Each entry has a `kind` and the fields that kind takes, which are the
fields of the Add a library form for it:

| Kind | What it is | Fields |
|---|---|---|
| `index` | a GUFI index on the importing computer | `indexRoot`; `sourceRoot`, `readThrough` optional |
| `folder` | a folder on the importing computer that its Studio indexes | `sourceRoot`; `indexDir` (an index folder of its own choosing), `depth` optional |
| `ssh` | a library on another system over ssh | `destination` (`host` or `user@host`), `port`, `sshOptions` (a list of words), `gufiBin`, `mode`; for `connect` `indexRoot`, with `sourceRoot` and `readThrough` optional; for `build` `sourceRoot`, with `indexDir` and `depth` optional |
| `pw` | a library on a cluster over pw ssh | `cluster`, `gufiBin`, `mode`, and the paths as for `ssh` |

`label` is optional for every kind (the last part of the path otherwise),
`mode` defaults to `connect`, and `depth` is one of the index depths
(`names` to `meaning-full`). Paths are absolute or under `~`, which is
the importing person's home here, or their home on the remote system.
`readThrough` is the host the files are read through (see "Reading
originals through another host"): `destination`, `port`, `sshOptions`,
`root`.

*Export:* **Export…** in Settings, Indexing, Libraries (`POST
/api/libraries/export`) writes every library ticked. The main folder is
in the file only when ticked, as a `folder` entry. A library built by the
platform's index workflow is the person's own and is left out with a
note. The file never has ids, build state, counts, dates of builds, caps,
user hashes, keys, tokens, passwords or settings. It also leaves out
what only works for the person exporting: the ssh options that name their
key files or ssh configuration (`-i`, `-F`, `IdentityFile`,
`CertificateFile`, `IdentityAgent`), and the folders
the Studio makes in their home on a remote computer (`~/.activate-studio`:
the default index folder of a build there, and GUFI it installed there),
which the importing Studio makes again. **Leave out the user name**, on by
default, writes `host` for `user@host`, here and in `readThrough`, and
drops `-l`, `User=` and the user names of jump hosts, so each person
connects as themselves and GUFI answers them with their own permissions.
A library this Studio built is written as a `folder` entry, for each
person's Studio to index again, unless **Share the indexes built here** is
ticked (`shareBuilt`, off by default, offered when a ticked library was
built): then it is written as an `index` entry (an `ssh` or `pw` entry in
`connect` mode for one built on another system, when its index path is
known and is not in `~/.activate-studio`), so each person connects this one
index and searches it as themselves. That is the administrator's export of
a root-built index ([`ACCESS-CONTROL.md`](ACCESS-CONTROL.md)).

*Import:* **Import…** in Settings, Indexing, Libraries, **Import a library
list from a file…** in Add a library, **Import a library list…** in the
Library's Add menu, and **Import a library list from your administrator**
on setup's Other libraries step take the file, chosen, pasted, or dropped
onto the import. On the Library page, where the Add menu offers
libraries, one `.json` file dropped onto the page or a folder in the
tree, under 1 MB and naming the list format, is checked with `POST
/api/libraries/import/check` first: a list opens the import with
**Add the file to the library instead** beside it, and anything the check
refuses is added as a file, as before. The Linux window's own drop, which
hands over paths and not the file's text, always adds files. `POST /api/libraries/import/check` reads it and answers
each entry normalized, with a state:

- *new*: ticked;
- *present*: already a library here, unticked: the same index directory
  or folder here, or the same host (whatever the user name), port and
  index directory or source folder, a path under `~` matching the one the
  connection test recorded; or the same as an earlier entry of the file;
- *invalid*: the entry cannot be read, with why: no kind or one this
  Studio does not know, a missing or bad field, or a file whose `version`
  is newer than 1. A file that is not a library list at all is refused
  whole; keys this Studio does not know are ignored;
- *unavailable*: not one this viewer may add here, with why: on a Studio
  where people sign in, an `index` or `folder` entry (libraries on that
  computer are the deployment's) and an `ssh` entry (libraries over ssh
  are for a Studio with sign-in off); a `pw` entry where pw ssh cannot be
  used now.

An `ssh` entry has an editable user name, the login the Studio runs as
when the file names none. **Add** then adds each ticked entry in turn
through the routes the forms use, so each one is validated and tested as
there: `POST /api/libraries` for `index`; `POST /api/libraries/build/check`
then `/build` for `folder`; `POST /api/user-libraries/ssh` or `/pw` for
`connect`; `POST /api/user-libraries/ssh/build/test` or `/pw/build/test`
then `/build` for `build`; and `PUT /api/libraries/:id/read-through` for a
`readThrough`. A build whose source folder does not exist fails; an
index folder it needs is created, as the form does once confirmed. Each
entry's line says Added (Building, for a build), Failed with the reason,
or Skipped.

## Libraries built from a folder on this computer

On a Studio with sign-in off, **Add a library**, "This computer", "Build
an index of a folder" indexes a folder here as the main folder is
indexed (`server/src/libraryBuild.ts`): `gufi_dir2index -x`, then text
extraction (`preextract` and `indexer/enrich.py`), then embeddings
(`indexer/embed.py`) where the model is installed, so full-text and
semantic search work there, as far as its index depth says (`depth`,
below the folder on the form: Meaning on documents when none is chosen;
at Names only `gufi_dir2index` runs). It
takes a source folder, an index folder (optional), a label, an
identifier, and the depth.

The phases run in order: the folders, then the text, then search by
meaning, shown on the card as **Computing search by meaning**. A first
build swaps its tree in as soon as the folders are indexed, so search
finds the library's names while the rest runs. Once the text is read the
library is probed again, so search reads its words while meaning is
computed, and a search by meaning names it with `not searched by meaning
yet: meaning is still being computed`. Its semantic map is made once it
is embedded. At a Meaning stop the form notes that search by meaning is
the slowest phase and that a folder of more than about 500,000 files
indexes faster at Text + OCR. Nothing counts the files before a build
(counting them takes a walk of the whole tree), so the note does not
depend on the folder's size and the default does not change with it.

**Suggested folders** (`GET /api/libraries/suggestions`, a single-user
install only) are offered above the folder to index, in Add a library
and in setup: Documents, Desktop and Downloads (on Linux the XDG user
folders from `~/.config/user-dirs.dirs`, else those names under the
home folder), and on macOS `~/Library/Mail`. Each is
`{ id, label, path, note, access }`, offered when it exists and is
neither the main folder nor a library's source. Documents, Desktop and
Downloads are not read to list them, since reading them on macOS asks
the person for access; Mail's folder is, and `access` is
`full-disk-access` when macOS refuses it, which
`POST /api/libraries/suggestions/full-disk-access` answers by opening
System Settings at Full Disk Access. A suggestion fills the folder and
label and runs Check.

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
phase and folder count also show on the library's card
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

A source folder its owner may not write, such as a read-only share, a
snapshot, or a tree unpacked with folders of mode 0555 and files of 0444,
is indexed whole, here, for the main folder, and on a remote computer.
`gufi_dir2index` gives each index folder the mode of its source folder as
it finishes that folder, before the folders inside it make theirs, so for
a user other than root the index folder of a 0555 folder could not be
written in: every folder inside it was left out (with a `mkdir ... failure`
line and an exit status of 0), and the build then failed moving the tree
into place. The folder scan the build and the sync already take lists the
folders whose owner may not write them; before `gufi_dir2index` runs the
index folders inside those are made, and after it those index folders get
read, write and search permission for their owner, with the source's group
and other bits kept (`server/src/indexModes.ts`). What GUFI records about
the source, the modes included, is unchanged. The sync, the text pass, a
change of depth, and removing the library then write in the index as for
any other. A tree an earlier version built of such a folder, with
folders left out and index folders of mode 0555, is opened and filled in
by the next sync, and removed whole by a rebuild or **Remove**. A folder
`gufi_dir2index` still could not make an index folder for is named as left
out, by its path in the source.

These libraries are read only in the Studio for now: no uploads, new
folders, renames, deletes, or labels from the Studio. Writing to them
would need the write routes, the extraction and preview caches, the label
overlay, and the incremental indexer, which all assume the main folder
today, to take a library's own folder, index, and caches; that is one
change across the indexing code, left for when it can be made in one
piece.

## Copies of a location

On a Studio with sign-in off, Transfers, **Keep a local copy and index
it…** (`web/src/components/KeepCopyDialog.tsx`) keeps a copy of a folder
in a Transfers location on this computer and indexes the copy. It lives in
Transfers, where those locations are chosen and browsed, because a copy is
the one library that is not read where it is: it is the way for a host
that takes SFTP only, a cloud drive or another rclone remote, where
nothing can be built or connected (`server-go/internal/libapi/copy.go`).
The location is a saved ssh location, a library host, a cluster, or an
rclone remote, chosen from the Transfers locations, with the folder
picked by browsing it. This is the way to make a library of a host
account that allows SFTP only, such as an upload drop, where no program
runs and nothing can be built, and of an rclone remote.

`POST /api/libraries/copy` takes `location` (a location id from `GET
/api/transfers/locations`), `path` (the folder in it, empty for its top),
and optional `label`, `id`, `every` (minutes between looks for changes:
0, 5, 15, 60, 360 or 1440; 15 by default; 0 looks only when asked), and
`depth` or `meaning`. It refuses a library, a folder on this computer
(build that in place) and CONDUIT (its paths cannot be listed), a
location that cannot be reached, and a folder that is not there. The
library is a built library (`build`, managed) whose folder is
`INDEX_BASE/libraries/<id>/files`, with its index and caches beside it,
and `copyOf: { location, path, label, every }` in `libraries.json`. Its
first build copies the folder, then indexes it as a folder on this
computer is indexed, so search, the Library tree, previews, Stats and the
assistant's tools read it as any built library.

Copying is a transfer (`transfers.Pull`): a sync from the location's
folder into the copy that deletes what the source no longer has, planned
and run as any transfer, so it uses the mover the location allows (rsync
where a host has it, rclone for a remote, Studio's own copy over ssh or
SFTP), lands through a staging folder, and checks what arrives. A look
that finds nothing to bring or remove leaves no transfer behind; one that
does is listed in Transfers, started by Studio (`startedBy: "library"`),
and the library's sync then indexes the folders that changed. Hidden
names are left out, as transfers leave them out. A sync takes at most
50,000 files, and the copy needs as much free space as the folder holds;
the plan refuses one that does not fit.

Studio looks for changes on the library's interval (`every`), one look
at a time per library, and **Update now** looks at once (`POST
/api/libraries/<id>/update`, answered at once). From that answer until
the look ends, `copyOf.looking` is true and the card says it is looking;
`copyOf.running` is the look's transfer once it has one. `PUT /api/libraries/<id>/copy` with `{ every }`
changes the interval. **Rebuild** looks and then builds the whole index
again. What the last look found is kept in
`INDEX_BASE/libraries/<id>/copy.json` and shown on the card: when it
looked, what it last brought or removed with a link to that transfer, and
why it failed. A location that is gone or cannot be reached leaves the
copy and its index as they were and says why. The library shows as
updating while a look that changes something runs, and cannot be removed
then. **Remove** deletes the copy, its index and caches, and never touches
the location.

The copy is read only. Transfers lists the library as a location that
takes no transfers, saying where to put files instead, since the next
look would remove anything put there. A library list leaves copies out,
with that reason, since the location belongs to this Studio's Transfers.
Copies use the Studio account's own access to the location, so they are
offered only with sign-in off.

## Buckets

Preview. On a Studio with sign-in off, **Add a library**, "Bucket"
indexes a bucket, or a folder in one, of an rclone remote from its
listing alone (`server-go/internal/libapi/bucket.go`). Any S3 compatible
store works, AWS or on premises (MinIO, Ceph and the like), since rclone
needs only its endpoint and keys, and so does any other store rclone
lists. The remote is one of the rclone remotes Transfers lists, used with
this computer's rclone configuration, which Studio never changes. No
object is read to index it and nothing is copied out of the store.

`POST /api/libraries/bucket` takes `location` (`rclone:<remote>` from `GET
/api/transfers/locations`), `path` (the bucket and folder in it, empty for
the remote's top), and optional `label`, `id` and `every` (minutes
between listings: 0, 60, 360 or 1440; 1440 by default; 0 lists only when
asked). It refuses any other kind of location, a folder that cannot be
listed, and a GUFI without `gufi_trace2index`, which the app ships. The
library is a built library (`build`, managed) with no folder here
(`sourceRoot` is null), its index at `INDEX_BASE/libraries/<id>/index/files`,
and `bucket: { location, path, label, every, text, fsid }` in
`libraries.json`. Its depth is names: a listing has names, sizes and times
only, so `PUT /api/libraries/<id>/depth` answers 409 for anything deeper
and search by meaning stays off. The objects' text is read only when
asked (below).

A listing is one `rclone lsjson -R --fast-list --no-mimetype
--use-server-modtime` of the folder. On S3 a fast list is one run of
ListObjectsV2 pages of 1,000 keys, so it takes about one request per
1,000 objects (100 million objects take about 100,000 requests). Each
entry is written to a GUFI trace (`server-go/internal/tracefile`),
grouped by folder in sorted runs on disk, so a listing of any size is
written in bounded memory and each folder appears once. Folders take mode
0700 and objects 0600, owned by the account the Studio runs as, and a
folder's time is that of its newest object. An entry's inode is a 63-bit
hash of its path, so an object keeps its inode from one listing to the
next while its path is the same, and text extracted from it, which is
keyed by inode and modification time, stays valid until the object
changes. Two paths in one folder that hash alike are told apart by
hashing again, in path order. `gufi_trace2index` builds the
trace into a staging folder, which is checked and then swapped in, so
search answers from the last listing while a new one runs. Paths longer
than GUFI holds, names "." or "..", and a name listed twice are left out
and counted. What the last listing found is kept in
`INDEX_BASE/libraries/<id>/bucket.json`: when it ran, objects, bytes,
folders, entries left out, the requests it took, the provider rclone's
configuration names, its seconds, and why it failed.

The Settings card shows how many objects and how much data the bucket
holds and when it was listed, and what a listing costs: the requests it
takes and, where the provider is AWS, their price at the S3 Standard
LIST rate ($0.005 per 1,000 requests), for one listing and for a month at
the library's interval. Other providers set their own prices, and a store
on premises has none. A ready bucket library is listed again when its
interval has passed (checked each minute), **List again** (`POST
/api/libraries/<id>/rebuild`) lists it at once, and `PUT
/api/libraries/<id>/bucket` with `{ every }` changes the interval. A
listing that fails leaves the last index in place and says why.

The Library tree, search and Stats read the index and send nothing to the
store. Opening a file reads that object alone: `rclone lsjson --stat` for
its size and time, then for a text file `rclone cat --count` for its
first 512 KB. A download, or a document preview, reads the whole object
up to 50 MB; a larger one answers 413 and points to Transfers. The
assistant's `read_original` reads plain text objects the same way. A
bucket library is not a Transfers location (its remote is), and a library
list leaves it out, with that reason, since the remote is in this
computer's rclone configuration. **Remove** deletes its index and never
touches the bucket.

### Reading text from a bucket

Off until asked, for each bucket library: every object read is a request,
and its bytes leave the store (`server-go/internal/libapi/buckettext.go`).
`GET /api/libraries/<id>/bucket/text` says what a pass would read now:
`objects` and `bytes` not yet read, `done` and `doneBytes` read before,
`over` and `overBytes` larger than a pass reads (`maxBytes`, 100 MB),
`other` the objects no reader reads, the `provider` as for a listing, and
`rate`, the bytes a second of the last pass that read at least a
megabyte (0 before one). Readable
objects are text files and documents (PDF, Office, OpenDocument, mail);
images are not read, since reading one is OCR of the whole image.

In Settings, Indexing, Libraries, the bucket card's **Read text** (Off, or
After each listing) asks first, with the estimate: what it reads, what is
left out, the price on AWS (data out at $0.09 a GB and GET requests at
$0.0004 per 1,000, S3 Standard), that a store on premises charges
nothing, and the time at the last measured rate (25 MB a second before
one). While a pass runs the card says **Reading text**, with its progress
and **Stop reading**; after, how many objects have text and what the last
pass read.

`PUT /api/libraries/<id>/bucket` with `{ text: true }` lists the bucket
and then reads its text; with `{ text: false }` a pass that is running
stops, nothing more is read, and the text read so far stays searchable.
`POST /api/libraries/<id>/bucket/text/stop` stops a pass, and the next
goes on from there. Each listing of a library with text on is followed by
a pass, in the same job, after the new index is swapped in, so search
answers from the new listing while the text is read.

A pass writes a file list of the readable objects of the index that no
pass has a record of at that modification time, and runs `index-extract
files --rclone <remote>:<path> --max-size 104857600 --no-ocr --jobs 8`
(docs/EXTRACTION.md), the program beside studio-server
(`STUDIO_INDEX_EXTRACT_BIN` to name another), one `rclone cat` request for
each object. Its artifact, `INDEX_BASE/libraries/<id>/text.db`, keeps every
object version's outcome, so an object is read once until it changes,
and a stopped pass goes on from where it stopped. What a pass added goes
into the library's spread tree (`INDEX_BASE/libraries/<id>/spread`, under
the library's fsid), and from there into the index's permission shards,
`studio_UR_<uid>_<gid>.db` beside each `db.db` (objects are indexed as
mode 0600 of the Studio's account), which search attaches as
external databases. A new listing's index gets its shards from the spread
tree before it is swapped in, so the text stays searchable through every
listing, and an object that changed or went loses its old text with the
listing that finds it. The library's `textPass` in `bucket.json` records
the last pass: when it ended, the objects it read by outcome, their
bytes, the requests, its seconds, how many objects have text now, any
helper it lacked, whether it was stopped, and why it failed.

The fsid names the bucket's records in a spread tree: the store's
endpoint host (for AWS `s3.<region>.amazonaws.com`, else the backend
type), the bucket and the library's prefix, as one folder name, lower
case, other characters than `a-z0-9.-` made `_`, at most 80 characters
with a hash of the whole when cut. It never uses the remote's name, which
each computer chooses, so the same bucket gets the same name wherever its
text is read.

## Indexes built on a remote computer

**Add a library**, "Remote computer", "Index a folder" builds
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
first. The depth starts at Meaning on documents when none is chosen,
stored with the library.

The Go version of the Studio runs no Python there. Its connection test
reads the system's platform with `uname` and offers text and meaning on
Linux x86_64 and aarch64 and macOS arm64; another system gets names only,
and the test says why. The first build sends the Studio's helper
(`studio-remote`, about 25 MB, kept in `~/.activate-studio/go/<platform>-<digest>`),
which runs the text pass (its own readers for PDF, Office and
OpenDocument files, pdftotext and tesseract where the system has them)
and the embedder, and for the meaning stops ONNX Runtime's library, taken
from the pinned onnxruntime wheel for that platform, with `model.onnx` and `tokenizer.json`
(kept in `~/.activate-studio/embed/<platform>-<digest of the pins>`). Each
is sent with a `SHA256SUMS` file, which the helper checks before the build
uses the folder. The index, its reports and the files described below are
the same; the extract cache entries start with the line the Studio's own
cache has. The rest of this section describes the Node version.

Search reads a remote index's vectors in its one-pass walk, which embeds
the query with the Studio's ONNX model and matches the directories whose
`gvecmodel` names that model, so the build there embeds with the same
model (`server/src/remoteEmbed.ts`). It needs onnxruntime, tokenizers and
numpy for the Python the build runs, and `model.onnx` and
`tokenizer.json`, as pinned in `indexer/embed_deps.json`: wheels are
pinned for CPython 3.11 to 3.13 on Linux x86_64 and aarch64 (manylinux
2.28, so glibc 2.28 or later) and macOS arm64. The connection test reads
the Python's platform as `fetch_embed.py` names it
(`linux-x86_64-cp312`) and says whether the model is there already,
will be sent with the first build (with its size, about 70 MB), or
cannot be used, and then what is used instead.

They are kept there in `~/.activate-studio/embed/<platform>-<digest of
the pins>`, installed once per version: `deps/` (the unpacked wheels),
`model/`, and `ok` once the packages import. As a build starts, the
Studio asks which versions are installed. When this one is not, the build
starts at once and the Studio sends what is missing while it runs, each
file over its own ssh or pw ssh call into
`~/.activate-studio/embed/<version>.files`, named as `fetch_embed.py`'s
cache names them; it marks `<version>.sending` before the build starts,
touches it as it goes, and removes it when done, and the build waits for
the mark to go (or to stand untouched for ten minutes) before its meaning
phase. The build then runs `fetch_embed.py --deps --no-readers --model
--cache <version>.files` there, which checks every file against its
SHA-256 and needs no network when all were sent, checks that the
packages import, and moves the install into place; the sent files are
then removed. Several builds may install at once; the first to finish
keeps its copy. Later builds send nothing.

The Studio takes the wheels from the bundle's `app/indexer/wheels` (the
platform bundle carries those for Linux x86_64) or its own
`INDEX_BASE/embed-wheels`, and the model from its own
`INDEX_BASE/models/minilm-l6`, each checked against its pin. What it does
not have it downloads into `INDEX_BASE/embed-wheels` with
`fetch_embed.py --download --platform <platform>`, where this computer
reaches the package index. With neither, `fetch_embed.py` on the remote
system downloads what was not sent, where that system reaches the
network. For a Studio and a remote computer that are both without network,
fill `INDEX_BASE/embed-wheels` on another computer with
`python3 indexer/fetch_embed.py --download --platform <platform> --cache <folder>`
and copy the folder there.

The document readers (pypdf, with typing_extensions for Python 3.10, xlrd
and olefile) are pinned in the same file under `readers`, and are pure
Python, so one set serves every platform and every Python the build
accepts. At every depth with text they go the same way to
`~/.activate-studio/readers/<digest of the pins>`, before the text pass,
which waits for them; a host with no poppler reads its PDFs with that
pypdf, with no network there. `fetch_embed.py --download-readers --cache
<folder>` fetches them for a Studio without network; the platform bundle
and the macOS app carry them in `app/indexer/wheels`.

The build records in `.studio-index.json` what it could not read:
`pdfWords`, `pdfVectors` and `vectorRows`, counted from the finished index
with `gufi_query`; `text` and `embed`, the reports of `enrich.py` and
`embed.py` (files and files with no text per document type, the PDF
reader used, passages, errors); `textWhy` and `vectorsWhy`, why a pass did
not run or stopped; and `depth`. The library's card shows these as a count
and a reason. A library whose vector tables hold nothing, though passages
were cut, counts as having no vectors. A Rebuild reads what the system can
run again, so a Python installed since the library was added is used. It
walks every folder again and reuses `.extract`, so only documents without
text are read again; a document that gave no text is never cached.

To see which case a library is in, on the system that holds it:

```
python3 --version; command -v pdftotext
cat <index folder>/.studio-index.json
ls ~/.activate-studio/builds/ ~/.activate-studio/readers/
```

Where the packages cannot be had or do not import there, the build uses
the GGUF model through sqlite-lembed (`minilm384`) where
`~/.activate-studio/models/minilm384.gguf` is on that remote computer, whose
vectors the walk does not match, and otherwise makes none. Each build
makes the index anew, so vectors made with the GGUF model by an earlier
build are made again with the ONNX model at the next build that has it.

The build runs on the remote system as a process of its own, detached
from the session (`setsid` where there is one, `nohup` always), so it
goes on when the session or the Studio stops. Its state is a folder there,
`~/.activate-studio/builds/<build id>`: the script, its phase, process id,
exit status, error, and logs. It runs `gufi_dir2index -x` into
`<index folder>/.build`; where Python
is there and the depth reads text, the Studio copies `enrich.py` into the
state folder and runs it (with `--no-ocr` at Text), keeping extracted
text in `<index folder>/.extract`, and at the meaning stops, where a GUFI
with the embedding extension is there too, `embed.py`, with the Studio's
ONNX model and `embed_onnx.py` beside it, or the GGUF model (above). It
then swaps the finished tree in, counts the index and writes
`.studio-index.json`, with `fullText` true only when that system's
`gufi_query` can read the text tables. The Studio follows the build as a
background job (`GET /api/jobs/<id>`), reading its state every few
seconds over the session and keeping the phase and folder count on the
library; after a restart the next listing follows it again, and an
update does not wait for it. When it ends the library is ready with its
counts, or says why not; a rebuild that fails leaves the index it had.
**Rebuild** runs the build again beside the current index, text and
vectors included, and swaps the result in at the end, so the index folder
holds two copies while it runs. It walks and indexes every folder again
and writes the text tables again; what it reuses is the text in `.extract`
(by modification time, so PDF, Office and OCR work is not repeated) and
the passages in `.embed-cache.db`, so a rebuild embeds only what is new.
There is no pass that adds meaning alone. **Remove**
forgets the library and leaves the index there.

The person who built such a library also writes in it from the Library,
as they do in My library: upload, new folder, rename, move and delete run
on that remote computer as their own account, over `pw ssh` with their own key, or
over plain ssh with the keys of the account the Studio runs as on a
Studio with sign-in off. The system's permissions decide what they may
change. The account is read from their session the first time they write
and kept with the library (`writer`); a session that later reaches another
account is refused. Unlike My library, what the Studio writes takes the
person's umask and the folder's default ACL, so a folder shared by group
stays shared. As in My library, a path outside the library's folder, or
one through a link, is refused before anything is written, the Library
lists the folders as they are on disk, and the index is built again about
20 seconds after the last change. An index connected as it is stays read
only, since the Studio does not build it, and so do libraries the
deployment provides. Search on these
libraries covers names, metadata, and, with extraction, full text, and
meaning where the build embedded with the ONNX model. A source folder
whose owner may not write it is indexed whole there too: when the log of
`gufi_dir2index` has `mkdir ... failure` lines, the build makes the index
folders inside each such folder and runs it again, and before moving the
tree into place gives the index folders read, write and search for their
owner (above).

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
platform's index workflow: Settings, Indexing, Libraries, **Add a library**, "A
system", "Index a folder", **Build with** "Workflow job": a cluster from their active clusters, a directory
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

A `gufi_query` on a remote computer, and a build there, runs with that
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
Studio's host reaches with plain ssh: Settings, Indexing, Libraries, **Add a
library**, "Remote computer", "A host over ssh…", "Connect an index", or the
setup wizard. The host is written `user@host`, an
alias from `~/.ssh/config`, or either with `:port`. A Host entry in
`~/.ssh/config` applies as it does in a terminal, ProxyJump, ProxyCommand,
Port, User and IdentityFile included, so a host behind a jump host or a
firewall is reached by giving its alias. The Studio runs
`ssh -T [options] -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -o Compression=yes -o ControlMaster=auto -o ControlPath="<folder>/%C[-<options hash>]" -o ControlPersist=600 [-p port] <destination>`
with the keys, agent, Kerberos tickets and `~/.ssh/config` of the account
it runs as, and keeps up to three sessions per destination, as it does
per cluster. The sessions to one host share a single ssh connection
through a socket, which stays open ten minutes after the last session
closes (`STUDIO_SSH_PERSIST`, in seconds, up to a day; 0 closes it with the
last session), so a new session does not log in again; `~/.ssh/config` is
not changed. The connection is compressed (a semantic map's vectors come
back as tens of megabytes); a library's own `-o Compression=no` comes
first and turns it off. When Search or Query opens, the Studio opens that connection
for each host of the viewer's libraries over ssh
(`POST /api/user-libraries/warm`, at most once a minute per host), with
`ssh -N -f`, which logs in and runs no command there, so the first search
after a long gap does not wait for the login. The socket's name is ssh's
`%C`, a hash of the host, port and user ssh connects to and its jump
hosts, after `~/.ssh/config` and the options are applied, followed by 8
hex characters of the library's SSH options when it has any. A library
given as `me@host` and one given as a Host alias that resolves to the same
account share one connection when their options are the same. Libraries
with different options get a connection each, since most options (a known
hosts file, an identity, a config file) apply only when ssh logs in, and a
session riding on another library's connection would not use them. Where
the folder is too long for that name, the name is 12 hex characters of the
destination, port and options instead. The folder is the first of these
that will do:
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
- GUFI's `gufi_query`, and for everything beyond the index a shell (bash).
  The test looks on the PATH of a
  non-interactive login, then in `~/.activate-studio/gufi/*/bin` (where
  the index workflow installs GUFI, newest first), `/usr/local/bin`,
  `~/usr/local/bin`, and `/opt/gufi/bin`; anywhere else goes in the GUFI
  bin directory field. A login that runs only `gufi_query` will do for a
  connected index (below). On
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

The same aggregate `gufi_query` counts the directories whose database
has the Studio's vector table (`gvec`, read from `tree.sqlite_master`),
so the index step says whether search by meaning covers the index, and
the library is saved with `vectors`. Search sends the query's vector to
the host only for a library with vectors. A library connected by
v1.188 or earlier was saved without `vectors`, so its search by meaning
returned nothing; it is now treated as unknown and searched by meaning,
and **Check** records what the index has. Vectors match only where the
directory's `gvecmodel` names the Studio's ONNX model
(`minilm-l6-mean-int8`); an index embedded with another model is
searched by name and words.

### SSH options

A library over ssh may carry its own ssh options, in the form's **SSH
options** field: for a jump host, a port, an identity, or a timeout that
are not in `~/.ssh/config`. For example `-J me@bastion.example -o
ConnectTimeout=20`. They go on every ssh the library makes: the connection
test, its sessions, a host that runs only `gufi_query`, the gufi_vt wrapper
and the warming above. They come first, so a library's value for a keyword
wins over the Studio's defaults (ssh keeps the first value it is given).
Allowed:

| Option | Takes |
|---|---|
| `-J` | jump hosts, `[user@]host[:port]`, several separated by commas |
| `-p` | a port, when the library has none of its own |
| `-i`, `-F` | a path on this computer, absolute or under `~/` |
| `-l` | a login name |
| `-o Keyword=value` | reaching and authenticating to the host: `ProxyJump`, `HostName`, `Port`, `User`, `IdentityFile`, `IdentitiesOnly`, `CertificateFile`, `IdentityAgent`, `UserKnownHostsFile`, `StrictHostKeyChecking`, `HostKeyAlias`, `GSSAPIAuthentication`, `GSSAPIDelegateCredentials`, `PreferredAuthentications`, `PubkeyAuthentication`, the algorithm lists (`Ciphers`, `MACs`, `KexAlgorithms`, `HostKeyAlgorithms`, `PubkeyAcceptedAlgorithms`), timeouts and keepalives (`ConnectTimeout`, `ConnectionAttempts`, `ServerAliveInterval`, `ServerAliveCountMax`, `TCPKeepAlive`), and a few more (`AddressFamily`, `Compression`, `CheckHostIP`, `IPQoS`, `LogLevel`, `RekeyLimit`, `UpdateHostKeys`, `VerifyHostKeyDNS`, the `Canonical*` keywords) |
| `-4`, `-6`, `-C` | nothing |

Refused, with the reason: anything that runs a command on this computer
(`ProxyCommand`, `LocalCommand`, `PermitLocalCommand`,
`KnownHostsCommand`) or replaces the command run there (`RemoteCommand`),
what the Studio sets itself (`BatchMode`, `ControlMaster`, `ControlPath`,
`ControlPersist`), port, agent and X forwarding, sending the environment,
and every other keyword. Values may not contain spaces, quotes or
backslashes, and nothing reaches a shell: the options go to ssh as
arguments. A setting the field refuses, a ProxyCommand say, goes in a Host
entry in `~/.ssh/config`, whose alias is then given as the host. The
options are checked again whenever a library is used, and a host gets a
shared connection of its own for each set of options.

The connection test names the jump host it went through, from the options
or from `~/.ssh/config` (read with `ssh -G`, which connects nowhere):
"Connected to me@lab-index through me@bastion.example, a Linux host."
Settings, Indexing, Libraries shows a library's options, and
`PUT /api/user-libraries/:id/ssh-options` with `{sshOptions}` changes them
for a connected index, after a connection test with the new ones passes.
An index built over ssh keeps the options it was built with.

### Routes

The ssh identity is the server account's, so libraries over ssh are
offered only on a Studio with sign-in off, which has one user; with
sign-in on, the routes answer 403. Routes:
`POST /api/user-libraries/ssh/test` and `POST /api/user-libraries/ssh`,
both with `{label?, destination, port?, sshOptions?, indexRoot, gufiBin?, sourceRoot?}`;
listing, refresh, and removal use the routes above, and list entries
carry `transport` (`pw` or `ssh`), `destination`, `port`, `sshOptions`
(as typed) and `jump`.

### Hosts whose login runs only gufi_query

Some sites give a login nothing but `gufi_query`: no bash, a restricted
shell, or a forced command (`ForceCommand`, or `command=` in
`authorized_keys`) that lets `gufi_query` through and refuses the rest. An
existing index there can still be connected, browsed, searched, and
queried. The connection test finds out: when the login refuses the
session (any exit but ssh's own 255 over plain ssh, or a "not found",
"restricted" or "not allowed" message over pw ssh), the host is marked to
run only `gufi_query`, and every command after that is its own ssh call
over the shared connection, written as one command line: the program
name bare, so a forced command that checks for a leading `gufi_query`
finds it, and each other argument as a POSIX shell word, in single quotes
where it holds anything but plain characters. Only `gufi_query` and
`gufi_sqlite3` are sent; anything else is refused in the Studio with a
reason. The library keeps the finding (`shell: false`), so after a
restart its first command is `gufi_query`, not another try at the shell.

On such a host:

- the test reports the ssh step as passed with the note "This host runs
  only gufi_query: connect existing indexes read only; building an index
  there is not available.", then `gufi_query --version`, then the index,
  counted with `gufi_query`, whose message says what is wrong when it is
  not an index (not a directory, no `db.db`, not readable);
- `gufi_query` is run by name from the login's PATH, or from the GUFI bin
  directory when one is given. A restricted shell refuses a command with
  a `/` in it, so leave that field empty there;
- paths are absolute: with no shell, `~` cannot be resolved there;
- the tree, the Details card, statistics, search by name, search by words
  where the index has full-text tables, Query through gufi_vt, and
  **Check** all run `gufi_query` alone;
- files cannot be opened or downloaded from that host, since that reads
  them with a shell; the source directory is left out, with a note. The
  index still answers for the text of its files (`read_kb_file`), and the
  library can name another host that has the files (see "Reading originals
  through another host");
- building an index there is refused at the build check, with the same
  note, since a build needs a shell, Python, and write access;
- the processor count is not read; queries run with 4 threads, or
  `STUDIO_REMOTE_THREADS`.

ssh's own notices on stderr, such as OpenSSH's post-quantum key exchange
warning or a host key being added, do not fail a step and are left out
of the messages. Kerberos (GSSAPI) works as it does from a terminal: the
Studio runs the system's ssh with the account's `~/.ssh/config`, and
`BatchMode` does not turn GSSAPI off. On macOS the server, started by its
LaunchAgent or by the app, is in the user's session and finds the
default `API:` credential cache; a `KRB5CCNAME` that the login shell sets
is passed on to it by the app's run loop.

The connection test also looks for the Studio's full-text tables
(`words`) in the index's top levels; where it finds them, search covers
the words in its files, on any host.

## Reading originals through another host

The assistant has three ways to look at a file, and a library can say where
the original file is when its own host does not have it.

- `search_kb` searches the index.
- `read_kb_file` returns the text the index holds for a file: the extracted
  text of an office document or PDF, the OCR or description of an image,
  and the contents of a plain text file. It works whenever the index can be
  read, including when the original is out of reach, and each result opens
  with a line naming its source (`[source: index; ...]`). The text of a
  plain text file is in the index (the words table, at most the first
  500,000 characters of the file, and only when the library was indexed
  with text), so `read_kb_file` returns that, and says it can lag behind the
  file until the next index update. For a plain text file the index has no
  text for (a library indexed by names only, a file type the text pass does
  not read, a file added since), the original is read instead, and the
  source line says so. For a document or an image with no text in the index,
  a library on this computer gives the text in its extraction cache; where
  there is none, the result says plainly that no text was read.
- `read_original` opens the original file: a plain text file as it is, and a
  document or image as the extracted text the Studio keeps for it on this
  computer (a read extracts nothing new). It tries the library's own
  location first, which is the folder on this computer or, for a library
  over ssh or pw ssh, its own host, and then the host the library names for
  reading originals. The result names where the file was read. When no host
  can give the file it names each host tried and what each said, for
  example that a host runs only `gufi_query`, that the file is not there, or
  that the connection was refused. `paths` (up to 25 files), `offset` and
  `library` work as they do in `read_kb_file`.

The case this is for is a site whose index lives where GUFI runs, on a host
whose login runs only `gufi_query` (see "Hosts whose login runs only
gufi_query"), while another host, such as a file transfer host, mounts the
same file systems and gives a normal shell. On the library's card in
Settings, Indexing, Libraries, **Originals via**, **Set** names that host:

- *Host*: `user@host`, or a Host alias from `~/.ssh/config`, with `:port`
  for a port other than 22. It is held to what a destination of `ssh` can
  be: letters, digits, dots, dashes and underscores, no leading `-`, no
  spaces, quotes or shell characters.
- *Folder on that host*: the library's folder there, an absolute path with
  no `..`, when it differs from the library's own folder. Empty means the
  same path as the library's own (its source directory, or for a library
  here with a source root, that folder).
- *SSH options*: as for a library over ssh (`-J`, `-i`, `-o Keyword=value`
  and the others the ssh options allow).

**Test connection** checks that ssh reaches the host, that its login gives a
shell (a host that runs only `gufi_query` is refused, since it cannot open
files), and that the folder is a directory the login can read. **Save** is
available once a test has passed with the same fields; **Remove** clears the
setting. The setting is kept with the library's other settings: in
`libraries.json` for a library added in Settings, and with the library's
entry for a user's library over ssh or pw ssh. A library named in
`STUDIO_LIBRARIES` can carry `"readThrough": {"destination": "user@host",
"root": "/path"}` in its entry; Settings shows it and does not change it.

The viewer uses the same places in the same order for a file's text
(`GET /api/kb/file`) and for **Download** (`GET /api/kb/download`). The
original-file previews that read the file from this computer (images in the
page, PDF pages, Office rendering) are not offered for a file that exists
only on the other host.

The host is reached the way libraries over ssh are: `ssh` as the account the
Studio runs as, with its keys, agent and `~/.ssh/config`, no prompts, a
connection shared between commands, and a limit on how long a read may wait.
A file's path is never read by a shell on either side: it is sent as an
argument of a small script, after the same checks every remote read makes (no
`..`, and under the library's folder). File names with spaces, quotes,
non-ASCII characters, and a leading `-` are read as they are. Because the
host uses the ssh keys of the Studio's account, it is offered only on a
Studio with sign-in off, as libraries over ssh are; with sign-in on the
routes answer 403 and a saved host is not used.

Routes: `PUT /api/libraries/<id>/read-through` with `{destination, port?,
sshOptions?, root?}` (it runs the connection test and saves only when it
passes; an empty `destination` clears the setting) and
`POST /api/libraries/<id>/read-through/test` with the same body, which
returns `{ok, steps}`. The main folder takes none, nor does an index built
of a folder on this computer, whose files are here. `read_original` is also
offered to MCP clients (`/api/mcp`), where it opens files the same way.

## Libraries over pw ssh

An existing GUFI index on one of the user's running clusters is connected
the same way, with `pw ssh <cluster>` in place of ssh: Settings,
Libraries, **Add a library**, "Remote computer", "Connect an index", or the
setup wizard's Other libraries step. The system is picked from the user's systems on the platform
(`GET /api/user-libraries/pw/systems`, which runs `pw clusters ls` with
the same key): running ones first, one that is off marked so (it has to
be started on ACTIVATE before it can be reached), the display name over
the name `pw ssh` takes, and "Another remote computer…" to type a name. The route
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
cluster or on a remote computer has `existing: false`, and one the Studio
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
  hits, each tagged with its library. Each library's search runs until
  the search time limit (Settings, Indexing, Sync and search,
  `searchTimeLimitSec`: 20 seconds by default, or
  `STUDIO_SEARCH_DEADLINE_MS` before one is saved; 0 is no limit), a
  library searched on its own too. One still running then is stopped,
  here or on its host, left out of the results with `timeLimit` in its
  `omitted` entry, and named in the answer's `slow`; the Search page links
  the setting and the chat's `search_kb` points to it. With `gufi_vt` as
  the engine, its one statement across libraries waits for every library;
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
`all`, `read_kb_file`, `read_original` and `list_kb_dir` take one library's
id, and `list_libraries` says which ids exist. A cluster library's files
are read and listed on the cluster.

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

Libraries on one host, reached the same way and asked the same plan,
share one location. Over ssh, "the same way" is where ssh lands once
`~/.ssh/config` and the library's options are applied, read with `ssh -G`
(which connects nowhere) and kept ten minutes: the user, host name and
port, the jump hosts and the proxy command, and the library's SSH options
themselves, so the location's ssh runs with options that are every
member's. A library typed as `me@host` and one typed as a Host alias whose
User is `me` are one host when neither has options, or both the same. Over pw
ssh it is the cluster and key. Such libraries share one location:
gufi_vt takes `index=` once per root, and hands the remote command every
root at the end of its line, so the host runs one gufi_query over all of
them. Each row goes back to the library whose index root its path is
under; grouped totals, which carry no path, are summed across libraries
anyway. Raw SQL is not stacked, since its rows have no known shape.
`STUDIO_STACK_REMOTE=0` gives each library its own location again. With two
libraries on one host this halves the remote commands a query makes, from
two checks and two gufi_query runs to one of each.

gufi_vt fails the whole statement when one location exits nonzero with no
rows, so each location is checked first (the index exists, and on a
cluster GUFI is installed), with one gufi_query over all of a location's
roots that answers a row per root; when a row is missing, each root is
asked on its own, and one that fails is left out and named.
gufi_vt runs the locations one after another. Without gufi_vt on the host,
for a plan whose SQL contains a double quote (gufi_vt's own delimiter), or
when the statement fails, each library answers on its own and the server
merges the rows. The response says which engine answered.

## The GUFI commands

Search, the Query page, and the chat's `search_kb`, `query_corpus` and
`list_kb_dir` tool blocks show the GUFI commands behind their results, as
a shell takes them, each with where it ran, when, how long it took, its
exit status, and its error output. "no such table" lines for the text or
vector tables, which a walk writes for each directory without them, are
counted as expected and kept apart from the rest. Name and full-text search run as one aggregate `gufi_query` (`-a 1` with
`-I`, `-E`, `-J`, `-K` and `-G`): each thread keeps rows until it has the
number asked for, and the merge returns that many, so a common word in a
large index sends back the rows shown and not every folder's matches.
A command on this host is shown as run. A cluster's command is shown
under the cluster's name, to run there after `pw ssh <cluster>`, and a
host's over plain ssh after `ssh` with the library's options. A gufi_vt
statement is shown as a here-document with gufi_vt's usual remote form,
`remote_cmd='pw', remote_arg='ssh', remote_arg='<cluster>'`, with the
cluster's GUFI added to the PATH there, so it runs from a terminal that
has GUFI and the pw CLI. The chat model does not receive the commands,
and a conversation shows them only to the person whose request ran them.
Settings, Indexing, Storage and logs, "Log GUFI commands" also writes every
command to a log file ([`REFERENCE.md`](REFERENCE.md), GUFI commands).

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

For buckets: reading their text through the platform next to the data,
bringing back only the index; images (OCR) and search by meaning; a
bucket read through an rclone mount; and
owners, groups and modes per entry for a Studio that people sign in to.
Writing to a library built from a folder on this computer (uploads, new
folders, renames, deletes, labels), which needs the indexing code to take
a library's own folder, index, and caches. Results by meaning from an
index on a remote computer. Enrichment for connected read-only libraries,
in sidecar databases beside the index on GUFI's external attach
mechanism, so full-text and semantic search work there too. Search across libraries as one gufi_vt statement; it runs
library by library. Refreshing a
user's library on a schedule, which needs the user's key at that time:
personal keys live in memory and the browser's sealed cookie, so until
delegated sign-in through the platform's OIDC applications arrives, a
refresh runs when the user asks for it.
