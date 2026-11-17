# Multi-user design note

Status: the first part, personal spaces and folder sharing, is in progress and ships as a Preview first (see [Personal spaces and folder sharing](#personal-spaces-and-folder-sharing)). The rest is design. This note collects the current state, the target, and the open questions, so the work can start from a shared understanding instead of a rediscovery pass.

## Where it stands

The service currently runs as one POSIX user, with the changes planned in this note not yet started. Every file the app writes (uploads, chat exports, seeded directories) is owned by that user, every query runs as that user, and the extracted text and vectors live inside the per-directory `db.db` files with the same ownership. `INDEX_BASE` is deployment-level state: settings, conversations, saved queries, the credential vault.

Platform identity (the `X-PW-User-Token` JWT from the session proxy, or a sign-in through an OpenID Connect provider) already separates people above the filesystem: conversations have owners, transcripts record who wrote them, model credentials are per user in the vault, and a shared key has a named provider. It does not separate anything at the filesystem: a group sharing a deployment this way has all its files roll up to the single service user.

The security statement that follows from this is in ARCHITECTURE.md section 10: point the app at a corpus its whole audience may read. What GUFI enforces today, and how to deploy one Studio per person for user and group access, is in [`ACCESS-CONTROL.md`](ACCESS-CONTROL.md).

## Target

1. **Ownership.** An upload is owned by the person who made it, with normal POSIX ownership and group membership, not by the service account. Same for chat exports and anything else the app writes on a user's behalf.
2. **Permission management.** Mode and group changes from the app (the Library context menu is the natural surface), so organizing access does not require a shell.
3. **Queries as the caller.** Search, query-builder, and chat-tool reads execute under the caller's uid. GUFI's tools support this; the server needs a per-user execution path where today it has one process identity.
4. **Need-to-know in the index.** A user's search must not return content from files they cannot read. GUFI gives metadata this property for free, because the index tree replicates source permissions and a walk cannot descend into a directory the caller cannot read. Extracted text and embeddings do not have it today, because they sit inside the shared per-directory dbs.

## Personal spaces and folder sharing

The first part of this note to be built, decided in November 2026. It runs a person's work as their own account over `pw ssh`. That needs no root on the cluster and no account mapping, because the platform logs the person in as their own account there.

*Credential:* Every read and write in a library read as the person runs over a `pw ssh` session opened with that person's own platform credential. That is the personal key they add for model access, or a pw CLI sign-in the Studio runs for them (`pw auth login --device`, kept in a credentials folder of their own with mode 700). The CLI sign-in renews itself: an access token lasts an hour, a refresh token 30 days from its last use, and the whole chain 90 days. Each refresh token works once, so the Studio runs one refresher per person. The Studio checks that the credential belongs to the signed-in person. Without such a credential it refuses the library, and it never substitutes the deployment's key or the service account. The CLI keeps an ssh key (`~/.ssh/pwcli`) and registers it with the account it acts for, so the Studio runs it, for each person, with a home folder of that person's own on the Studio's host. The key is then theirs alone, and signing out removes it. A delegated, scoped token from the platform's OpenID Connect provider would replace both credentials, and has been requested from the platform.

*My space:* The person picks a folder on the Studio's cluster, `~/studio` by default. The cluster is the one a workflow deployment runs on (`pw://PW_USER/PW_POOL_NAME`), or `STUDIO_HOME_CLUSTER`. A check run as the person comes first. It refuses a folder they do not own, one that a group or every account can write, and one under a folder that another account could change: a folder owned by another account, or one a group or every account can write without the sticky bit, where someone else could rename the space away and put a folder of theirs in its place. It also refuses the home folder itself, which holds the Studio's own files. A folder that does not exist yet is made with mode 700 once the person confirms. The Studio keeps `~/.activate-studio` at mode 700 and builds the index there as the person, so only they can open it. GUFI runs from the person's own copy, which the Studio puts in that folder the first time (about 9 MB), or from a GUFI that only root can change. A copy on a shared disk that others can write is never used, because whoever changes it would run code as each person who uses it. The space appears as their own library. Reads (Library, Search, query builder, file reader, assistant tools) and writes (upload, new folder, rename, move, delete) run as the person, so new files are theirs. The Library lists a space's folders as they are on disk, and about 20 seconds after the last change the Studio builds the index again, so search catches up. Copying inside a space and adding a file from a web address come later.

*Shared libraries read as the viewer:* A library the deployment provides has a setting for whom it is read as, the viewer or the service. Read as the viewer, every search and read of it runs through the person's own session, so its permissions apply to each person. Read as the service, everyone sees what the service account can see, as before.

*Folder sharing:* **Share…** on a folder of a space gives an account or a host group the owner belongs to read, or read and write, on it. The Studio applies it as the owner. It sets a POSIX ACL on the folder and everything in it, and a default ACL so new files inherit. It adds traverse-only access on each folder above it that the owner owns, the home folder included, and refuses when a folder above it belongs to someone else and is closed to the recipient. Traverse-only access lets the recipient pass through a folder without listing it, though they can open a folder in it that is open to every account if they know its name. Where an existing ACL keeps the owning group narrower than its mode, sharing leaves it so. Recipients find the folder under **Shared with me**, a list kept in a share registry, and add it as a library of their own. The Studio indexes it as the recipient, into their own `~/.activate-studio`, so their index holds what they can read and the owner's index stays closed to them. Reads and writes in it run as the recipient, and a write by either side indexes the folder again for both. The registry is for finding shares and grants nothing. Taking a share back removes the ACL entries, the traverse entries that no other share needs, the recipients' libraries of it, and their index of it where the Studio can still reach their account. What a recipient copied while they had access stays theirs. Removing a space takes its shares back.

*File systems without ACLs:* Some shared disks refuse `setfacl`, for one NFSv4 mounted with `sec=sys` and no NFSv4 ACL tools installed. The space check tests the folder's file system, and where it refuses ACLs the owner can share a folder with every account for reading, by mode bits. Sharing with one group by mode bits would mean changing the group of the folders above it, and comes later.

*Single files:* Sharing one file comes later. The Studio's own text tables hold a folder's text in one database, so granting one file through the index would expose the text of the files beside it. It arrives with the external database writer, whose permission shards, extended to ACL sets, would keep a file's text where only its readers can open it.

### Aligning the shared library with POSIX

The deployment's shared library joins the same framework, as a space shared with everyone. As far as POSIX allows:

- The kernel decides. Read as the viewer, every search and read runs as the person, and the Studio keeps no permission list of its own.
- Files are owned by the people who add them, and take the library's group where its folders carry the setgid bit. Files already there keep their owner, the service account, and the operator manages their sharing.
- Sharing with everyone is expressed in POSIX terms, as a host group that holds the audience (with setgid folders and a default ACL so new files inherit), or as read for other accounts where the system's accounts are the audience. The library's sharing dialog shows which one applies.
- The index mirrors the source. Each index folder takes its source folder's mode, group and ACL. Without root, the index folders stay owned by the service account that builds them.
- Text follows file permissions. A shared library others read is built with `STUDIO_TEXT_ACCESS=file`, and the check reads ACLs as well as modes. A file narrower than its folder is found by name only until the external database writer gives it a shard of its own.

Where it stops:

- The service account builds the shared index, so the index holds what that account can read. A folder someone limits to themselves leaves the shared index, and personal material belongs in a personal space.
- Folder names are visible to whoever can read the parent, as with `ls`.
- The server process handles what a search returns and what the assistant reads, and keeps conversations in its data folder. Other users cannot see a person's space, but the account the Studio runs under can see what passes through it. One Studio per person, as in [`ACCESS-CONTROL.md`](ACCESS-CONTROL.md), is the arrangement without that.

## The mechanism for point 4: permission-permutation external dbs

GUFI's external-db design stores per-file extracted content in the index tree under the same access control as the source. The pieces:

- **Shards in the tree.** External dbs live inside the GUFI index tree, one set per directory, named by permission permutation: `ext_ur.db` for files the directory owner can read, `ext_gr.db` for the directory group, `ext_or.db` for other-readable files, and `ext_<uid>_<gid>r.db` for files outside those categories. Each shard's own owner, group, and mode encode the access of the files whose records it holds, so the filesystem enforces who can open which shard, and tree traversal permissions cover the rest.
- **Version-aware keys.** Records are keyed `fsid.inode.mtime`, unique to filesystem, file, and version. A query for files with no ext record at the current mtime gives the re-extraction work list, and a changed file invalidates its records by the same key.
- **Three tables per shard.** A plain status table (`ext_file`, per-file extraction state), an fts5 bm25 table (porter tokenizer, chunked with page and position metadata), and a vec0 table (4096-dim embeddings with the producing model's name and dimensions recorded per chunk, so mixed models coexist).
- **Staging through a spread tree.** Extraction output lands in a permanent staging tree sharded by fsid.inode range, then merges into the per-directory shards. Extraction is a workflow over a GUFI query ("everything matching this pattern with no current ext record"), parallelized by inode range, and the extractor can differ per file type, with the producing model recorded per chunk.
- **Query time: `gufi_vt`.** A SQLite virtual table runs the walk with a thread pool: per-thread intermediate tables, external records copied in per directory (the thread simply cannot open shards the caller cannot read), bm25 rank joined to vec0 cosine distance for hybrid retrieval, then a global aggregate. The caller composes all of it in SQL.

This resolves two earlier open questions. vec0 works inside the shards (the reference schema is a vec0 table with high-dimension float32 columns). Derived caches become ext db records instead of files: extracted text is a bm25/status record, and rendered PDF pages either move into the shards as blobs or take the same permission-shard naming on disk.

Mapping to this codebase: the per-directory `words` and `gvec` tables become permission shards; the enrichment pass becomes a query-driven extraction workflow keyed by `fsid.inode.mtime` instead of an mtime-cached walk; and the search paths move from per-directory fan-out in application code to `gufi_vt` composition. Related upstream GUFI work: ext dbs combined with rollups, and ANN with neighborhoods merged with treesummaries.

## Multisite and the virtual data lake

Two further directions extend past a single deployment and constrain the multi-user design:

- **Virtual data lake**: emulate a lakehouse from source data instead of copying into one, with layout metadata (headers, footers, page statistics) held in GUFI and byte ranges read at query time, access control current because it is the filesystem's. The Studio's enrichment is the unstructured half of that picture; the structured half follows the same principle.
- **Cross-site**: a Studio per site over a site-local GUFI tree, federated at the query layer instead of by copying corpora. Site indexes stay under site access control; a multisite query is a fan-out of `gufi_vt` queries with results merged, structurally the same as a single-site query's fan-out over directories. The building blocks exist: per-site persistence (corpus and index on durable storage, app relaunched by workflow) and the compute-node mode (app plus model in one scheduler job over a site corpus).

None of this is scheduled. It is recorded here as a constraint on the multi-user design.

## Identity mapping

Where it stands: ACTIVATE is an OpenID Connect provider (a feature preview; an organization administrator registers applications), and the Studio is a generic OIDC client of any provider (ARCHITECTURE.md section 7, DEPLOY.md). A signed-in person gets the same `req.user` as one the session proxy identifies. The provider's tokens are identity only: they open the provider's userinfo endpoint and nothing else, so acting as the person on the platform stays on their own platform credential. Execution as the person's host account over `pw ssh` is the first part being built ([Personal spaces and folder sharing](#personal-spaces-and-folder-sharing)); the lookup and execution described below are for hosts reached without it.

The provider's username is not assumed to be a host account. ACTIVATE releases `sub`, `name`, `preferred_username`, `email` and `groups`, and no uid or gid, and its username does not always match the login on a given cluster. The operator names the claims that carry the account and, where a provider emits them (Keycloak backed by LDAP can release `uidNumber` and `gidNumber`, for one), a numeric uid and gid: `AUTH_ACCOUNT_CLAIM`, `AUTH_UID_CLAIM`, `AUTH_GID_CLAIM`. Whatever is present is recorded on `req.user` as `account`, `uid` and `gid`, and absent values stay absent. A mapping per host (this person is `jdoe` on one cluster and `jd1234` on another) is expected to sit beside this and is not implemented; the per-host lookup below should take the recorded claims as input and never fall back to the username silently.

The working assumption: authentication is OIDC (through ACTIVATE, carrying the CAC-authenticated user, or standalone against an IdP directly), and the authenticated user has a valid uid and gid on the cluster holding the knowledge base. That matches how these clusters are actually run, where the platform account and the host account are the same person, and it removes the hardest design problem, since the mapping is a lookup rather than a provisioning scheme.

What the lookup and execution need:

- **Resolving the account**: the claim the operator names (`AUTH_ACCOUNT_CLAIM`, or a per-host mapping where accounts differ between hosts) resolves to the host account with getpwnam, and a uid claim, where there is one, is checked against the result. The username is used only when the operator names it as the account claim. A user who authenticates but has no host account is refused filesystem writes and reads beyond what the service user could do, with a message that says why.
- **Acting as the uid**: the service needs a mechanism to execute as the caller: per-user worker processes, a setuid helper, or a privileged wrapper of the kind GUFI's own multi-user tooling uses. This is the main implementation decision for step 2.
- **Standalone**: same design, with the IdP as the source of the claim instead of the platform's session proxy. The OIDC client is in place, so a standalone deployment is configuration.

Deployments without host accounts for their users stay supported as what they are: shared-user mode, identity separation above the filesystem only, and labelled that way.

The vault, conversations, and chat filters already key on the verified subject, so nothing above the filesystem changes shape; the new work is entirely at and below the filesystem boundary.

## Deployment-state questions to settle during design

- The credential vault and settings are deployment-level today and can stay that way, but the vault's threat model changes when host accounts are real: a per-user key could move into files owned by that user instead of one vault file owned by the service.
- Index writes (uploads triggering incremental indexing, the sweep) currently run as the service user. With user-owned files, the indexer either runs privileged enough to read everything it indexes, or per-user index passes write only what that user can read. The permission-permutation shards assume the indexer can read the content it shards, which points at the first option, run carefully.
- The seeded starter layout and README should be owned by the deploying user, which they already are; nothing changes there.

## Sequencing

A reasonable order, each step useful on its own:

1. Ownership on writes (uploads, exports) plus permission management in the Library. No query changes yet; this makes the filesystem truthful about who added what.
2. Queries as the caller's uid for search and the query builder. Metadata need-to-know arrives here for free.
3. Extracted content and vectors into permission-permutation external dbs. Content need-to-know arrives here.
4. Derived caches brought under the same scheme.

Personal spaces and folder sharing deliver steps 1 and 2 for libraries read as the person over `pw ssh`, including shared libraries set to be read as the viewer. Steps 3 and 4 follow with the external database writer.

The existing shared deployments are the live cases to design against: they should keep working unchanged at every step, gaining ownership first and need-to-know later, without a flag day.
