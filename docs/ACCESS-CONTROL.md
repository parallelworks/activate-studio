# Access control

The Studio's retrieval for its assistant reads the GUFI index itself. Text and vectors sit in SQLite databases inside the index tree, so there is no separate vector database to run, no copy of the documents to keep in sync, and the index carries the source's POSIX permissions. Who can find what is decided by those permissions, through GUFI. The Studio keeps no access list of its own. Every search, query and assistant tool runs `gufi_query` as the account the Studio runs under (or, for a library on another system, as the account ssh logs in to), and GUFI shows that account only what its permissions allow. This page describes how that works, what it covers today, and how to deploy for user and group level access.

## How GUFI enforces permissions

`gufi_dir2index` builds an index tree with the shape of the source: one index folder per source folder, each holding a `db.db` with the metadata of that folder's entries, including each entry's owner, group and mode. Each index folder takes the mode of its source folder. When an administrator builds the index, the index folders also take the source folder's owner and group, so the index tree carries the same access as the source. An index built by an ordinary user is owned by that user and serves that user.

`gufi_query` walks the tree as the user who runs it. A folder that user cannot open is not entered, and the walk continues elsewhere, so a query returns the folders the user could list in the source and nothing below a folder they could not. As with `ls`, the names and metadata of files in a folder the user can read are visible even when a file's own mode would refuse reading its contents.

Rollups keep this property. `gufi_rollup` merges a folder into its parent only when every child folder grants the same access as the parent (all world readable, or the same owner and group with matching modes), so a rolled up index answers each user as the unrolled one would. Labels are stored as extended attributes, and GUFI keeps an xattr that not every reader of the folder may see in a per-uid or per-gid database, which only that user or group can open.

The GUFI documentation describes the site deployment this assumes: indexes on an index server, the source system's users and groups present there, users connecting over ssh as themselves, and `gufi_jail` limiting their logins to GUFI's commands.

## Text and vectors

Search by words and by meaning reads extracted text and vectors, which can sit in two places, with different granularity.

| Where | Who can read it | Granularity |
|---|---|---|
| The Studio's own tables (`words`, `gchunks`, `gvec`) inside each folder's `db.db`, written when the Studio builds a library | anyone who can open that folder's `db.db` | folder |
| GUFI external databases beside `db.db` (`ext_*.db`), written by GUFI's extraction workflow (`gufi_spread_tree.sh`, `gufi_spread_to_extdb.sh`) | only users who could read the files whose text the database holds: the workflow sets each database's owner, group and mode to match | file |

With the Studio's own tables, a folder is the unit of access: whoever can read a folder's index can search the text of every file in it, including a file whose own mode would refuse them. Where permissions are set by folder, which is how most shared drives are organized, that is the same answer the file system gives.

External databases give file level access. The Studio reads every database beside `db.db` whose name matches `STUDIO_EXTERNAL_DB_PATTERN` (default `\.db$`) in the same walk, with `gufi_query --external-copy` ([`REFERENCE.md`](REFERENCE.md), Search). A database the user cannot open is left out of that folder's answer, and the folder's other databases still answer. So a search by words or by meaning returns passages only from files the user can read. Measured with GUFI 0.8.0: of two databases in one folder, one readable and one not, the query returned the rows of the readable one and exited 0; an index folder of mode 000 was skipped with a "Permission denied" line, and the rest of the tree answered.

The assistant's file tools (`search_kb`, `read_kb_file`, `read_original`) run the same way, through `gufi_query` or the file system as the Studio's account, so a file that account cannot read never reaches the model through them.

## Deploying for user and group access

*One Studio per person.* The Mac app, the Linux app, and a Studio each person launches as their own ACTIVATE session all run as that person, so every search, query and assistant read runs with that person's uid and groups. User and group permissions are enforced today in this arrangement, in two ways:

- Each person adds the shared folders as a library and the Studio builds the index as them. The index holds only what that person could read, and stays in their own Studio's data folder. This needs nothing set up centrally, and each person's Studio indexes the drive again.
- An administrator builds one index of the shared drive, as root, on an index server or on the drive itself, with text and vectors written as GUFI external databases. Each person connects that index in **Other libraries**, as a folder on their own computer (an NFS mount of the index, say) or over ssh to the index server as themselves, where a login restricted by `gufi_jail` to `gufi_query` is enough. One index serves everyone, and each person sees their part of it. To make that one step, the administrator exports the shared index's library entry as a library list (Settings, Indexing, Libraries, **Export…**, with the user name left out), and each person imports the file into their own Studio, in setup or in Settings, which connects them as themselves ([`LIBRARIES.md`](LIBRARIES.md#library-lists)).

*One Studio shared by several people.* A Studio that several people use through the ACTIVATE session proxy or an OpenID Connect sign-in runs as one service account. Every query runs as that account, so every user sees what that account can read. Sign-in separates conversations, model keys and attribution, and does not separate file access. Point a shared Studio at material its whole audience may read, or run one per group. Running each query as the signed-in person's own uid is the plan in [`MULTI-USER.md`](MULTI-USER.md), and is not scheduled.

## What the Studio does not do yet

- Build GUFI external databases itself. The Studio's own extraction writes its folder level tables; writing per-permission external databases is step 3 of [`MULTI-USER.md`](MULTI-USER.md). Until then, file level access for text and vectors comes from GUFI's extraction workflow.
- Run a query as anyone other than the account it runs under, as described above for a shared Studio.
- Change modes or groups from the Library. Uploads, new folders and moves are made as the Studio's account, with that account's default mode.
