# Multi-user design note

Status: design, not yet scheduled. This note collects the current state, the target, and the open questions, so the work can start from a shared understanding instead of a rediscovery pass.

## Where it stands

The service runs as one POSIX user. Every file the app writes (uploads, chat exports, seeded directories) is owned by that user, every query runs as that user, and the extracted text and vectors live inside the per-directory `db.db` files with the same ownership. `INDEX_BASE` is deployment-level state: settings, conversations, saved queries, the credential vault.

Platform identity (the `X-PW-User-Token` JWT, and the ACTIVATE OIDC flow in progress that will carry the CAC-authenticated user) already separates people above the filesystem: conversations have owners, transcripts record who wrote them, model credentials are per user in the vault, and a shared key has a named provider. It does not separate anything at the filesystem: a group sharing a deployment this way has all its files roll up to the single service user.

The security statement that follows from this is in ARCHITECTURE.md section 10: point the app at a corpus its whole audience may read.

## Target

1. **Ownership.** An upload is owned by the person who made it, with normal POSIX ownership and group membership, not by the service account. Same for chat exports and anything else the app writes on a user's behalf.
2. **Permission management.** Mode and group changes from the app (the Library context menu is the natural surface), so organizing access does not require a shell.
3. **Queries as the caller.** Search, query-builder, and chat-tool reads execute under the caller's uid. GUFI's tools support this; the server needs a per-user execution path where today it has one process identity.
4. **Need-to-know in the index.** A user's search must not return content from files they cannot read. GUFI gives metadata this property for free, because the index tree replicates source permissions and a walk cannot descend into a directory the caller cannot read. Extracted text and embeddings do not have it today, because they sit inside the shared per-directory dbs.

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

The working assumption: authentication is OIDC (through ACTIVATE, carrying the CAC-authenticated user, or standalone against an IdP directly), and the authenticated user has a valid uid and gid on the cluster holding the knowledge base. That matches how these clusters are actually run, where the platform account and the host account are the same person, and it removes the hardest design problem, since the mapping is a lookup rather than a provisioning scheme.

What the lookup and execution need:

- **Resolving the account**: the OIDC subject or preferred_username resolves to the host account (getpwnam on the claim, with a claim-to-account mapping configurable for sites where the two differ). A user who authenticates but has no host account is refused filesystem writes and reads beyond what the service user could do, with a message that says why.
- **Acting as the uid**: the service needs a mechanism to execute as the caller: per-user worker processes, a setuid helper, or a privileged wrapper of the kind GUFI's own multi-user tooling uses. This is the main implementation decision for step 2.
- **Standalone**: same design, with the IdP as the source of the claim instead of the platform's session proxy. The app already verifies JWTs from a JWKS URL, so standalone OIDC is configuration plus the token acquisition flow, not a second identity system.

Deployments without host accounts for their users stay supported as what they are: shared-user mode, identity separation above the filesystem only, and labelled that way.

The vault, conversations, and chat filters already key on the JWT subject, so nothing above the filesystem changes shape; the new work is entirely at and below the filesystem boundary.

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

The existing shared deployments are the live cases to design against: they should keep working unchanged at every step, gaining ownership first and need-to-know later, without a flag day.
