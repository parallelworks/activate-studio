# Multi-user design note

Status: design, not yet scheduled. This note collects the current state, the target, and the open questions, so the work can start from a shared understanding instead of a rediscovery pass. Correspondence with Gary Grider (LANL, GUFI) that shaped it is in the knowledge base under `emails/`.

## Where it stands

The service runs as one POSIX user. Every file the app writes (uploads, chat exports, seeded directories) is owned by that user, every query runs as that user, and the extracted text and vectors live inside the per-directory `db.db` files with the same ownership. `INDEX_BASE` is deployment-level state: settings, conversations, saved queries, the credential vault.

Platform identity (the `X-PW-User-Token` JWT, and the ACTIVATE OIDC flow in progress that will carry the CAC-authenticated user) already separates people above the filesystem: conversations have owners, transcripts record who wrote them, model credentials are per user in the vault, and a shared key has a named provider. It does not separate anything at the filesystem: an AFRL group currently shares a deployment this way, and their files all roll up to the single service user.

The security statement that follows from this is in ARCHITECTURE.md section 10: point the app at a corpus its whole audience may read.

## Target

1. **Ownership.** An upload is owned by the person who made it, with normal POSIX ownership and group membership, not by the service account. Same for chat exports and anything else the app writes on a user's behalf.
2. **Permission management.** Mode and group changes from the app (the Library context menu is the natural surface), so organizing access does not require a shell.
3. **Queries as the caller.** Search, query-builder, and chat-tool reads execute under the caller's uid. GUFI's tools support this; the server needs a per-user execution path where today it has one process identity.
4. **Need-to-know in the index.** A user's search must not return content from files they cannot read. GUFI gives metadata this property for free, because the index tree replicates source permissions and a walk cannot descend into a directory the caller cannot read. Extracted text and embeddings do not have it today, because they sit inside the shared per-directory dbs.

## The mechanism for point 4: permission-permutation external dbs

GUFI has a system-level external db mechanism built for exactly this. Content that cannot be recomputed at walk time goes into external dbs beside the index, sharded by the permission permutations present in each directory: each shard's file mode encodes the access of the files whose records it holds, so the filesystem itself enforces who can open which shard. At query time GUFI virtually appends the external db records into a table and attempts to open each shard as the caller; shards the caller cannot open contribute nothing.

Applied here: the fts5 `words` rows and the vec0 chunks for a file would move into the shard matching that file's permissions, and a caller's full-text or semantic query would only ever see content from files they could read directly. The label overlay could take the same treatment, though xattr labels already inherit access control from the file.

Gary has offered to walk through the mechanism in detail. Standing questions put to him:

- Does a vec0 virtual table work inside an external db shard the way it does in the tree dbs? The embedding path depends on `gufi_sqlite3` loading sqlite-vec and sqlite-lembed against whatever db it opens.
- What is the right treatment for derived caches that live outside the dbs (rendered PDF pages under `pdf-pages/`, extracted text under `extract/`)? They leak content the same way the shared dbs do; either they move into the sharded dbs or they need an equivalent permission scheme on disk.

## Identity mapping

The working assumption: authentication is OIDC (through ACTIVATE, carrying the CAC-authenticated user, or standalone against an IdP directly), and the authenticated user has a valid uid and gid on the cluster holding the knowledge base. That matches how these clusters are actually run, where the platform account and the host account are the same person, and it removes the hardest design problem, since the mapping is a lookup rather than a provisioning scheme.

What the lookup and execution need:

- **Resolving the account**: the OIDC subject or preferred_username resolves to the host account (getpwnam on the claim, with a claim-to-account mapping configurable for sites where the two differ). A user who authenticates but has no host account is refused filesystem writes and reads beyond what the service user could do, with a message that says why.
- **Acting as the uid**: the service needs a mechanism to execute as the caller: per-user worker processes, a setuid helper, or a privileged wrapper of the kind GUFI's own multi-user tooling uses. This is the main implementation decision for step 2.
- **Standalone**: same design, with the IdP as the source of the claim instead of the platform's session proxy. The app already verifies JWTs from a JWKS URL, so standalone OIDC is configuration plus the token acquisition flow, not a second identity system.

Deployments without host accounts for their users (the current AFRL pattern) stay supported as what they are: shared-user mode, identity separation above the filesystem only, and labelled that way.

The vault, conversations, and chat filters already key on the JWT subject, so nothing above the filesystem changes shape; the new work is entirely at and below the filesystem boundary.

## Deployment-state questions to settle during design

- The credential vault and settings are deployment-level today and can stay that way, but the vault's threat model changes when host accounts are real: a per-user key could move into files owned by that user instead of one vault file owned by the service.
- Index writes (uploads triggering incremental indexing, the sweep) currently run as the service user. With user-owned files, the indexer either runs privileged enough to read everything it indexes, or per-user index passes write only what that user can read. The permission-permutation shards assume the indexer can read the content it shards, which points at the first option, run carefully.
- The seeded starter layout and README should be owned by the deploying user, which they already are; nothing changes there.

## Sequencing

A reasonable order, each step useful on its own:

1. Ownership on writes (uploads, exports) plus permission management in the Library. No query changes yet; this makes the filesystem truthful about who added what.
2. Queries as the caller's uid for search and the query builder. Metadata need-to-know arrives here for free.
3. Extracted content and vectors into permission-permutation external dbs. Content need-to-know arrives here. Requires the walkthrough from Gary and answers to the two standing questions.
4. Derived caches brought under the same scheme.

The AFRL shared deployment is the live case to design against: it should keep working unchanged at every step, gaining ownership first and need-to-know later, without a flag day.
