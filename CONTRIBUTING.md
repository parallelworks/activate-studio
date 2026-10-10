# Contributing

Contributions are welcome: bug reports, fixes, new extractors, viewers, documentation. The Studio is licensed under Apache 2.0, and a contribution is accepted under the same license.

## Getting it running

The README's quick start and [`docs/MACOS.md`](docs/MACOS.md) cover setup. The short version:

```
pnpm install
pnpm build
pnpm start          # http://localhost:4080, over knowledge-base/ beside the code
```

Node 26 or newer and pnpm are required. GUFI is needed only for the indexed search surfaces; everything else runs without it.

## Working in the repository

This is a pnpm workspace: `server/` is the Fastify API and `web/` the React client. Add or upgrade a dependency with pnpm from the repository root, for example `pnpm --filter @activate-studio/web add <package>`, and commit `pnpm-lock.yaml` with it. Running `npm install` inside a package leaves the lockfile stale and CI, which installs with a frozen lockfile, will fail.

New dependency versions must be at least 24 hours old; the workspace enforces this.

| Directory | Holds |
|---|---|
| `server/` | the Fastify server: knowledge base API, search, the assistant's tool loop, ingestion, indexing, queries, workflows, agents |
| `web/` | the React interface |
| `indexer/` | the GUFI build, full re-indexing, and text, OCR, and image extraction |
| `testdata/` | a synthetic corpus and the end-to-end extraction test |
| `deploy/` | the ACTIVATE workflow, the bundle builder, and the container definition |
| `docs/` | the user guide (`HELP.md`) and the other documents listed in the README |

Architecture and design notes live in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), and the multi-index design in [`docs/LIBRARIES.md`](docs/LIBRARIES.md).

## Tests

```
pnpm test
```

That builds the server and runs its suite (Node's test runner) and the web suite (Vitest). CI runs the same on Linux and macOS for every pull request. A change that alters behavior comes with a test that fails without it.

## Pull requests

Open the pull request from a branch on your fork. Keep each one to one change.

The description matters more than usual here. The release notes and `CHANGELOG.md` are generated from pull request descriptions, so write it as the change note a reader will see: what was wrong or missing, what changed, and how you verified it. The title becomes the squashed commit's subject.

Pull requests are squash-merged and need two approving reviews from maintainers, with review threads resolved. First-time contributors' CI runs start after a maintainer approves them.

Commit messages must not carry automated-assistant attribution trailers; the organization's rules reject them at merge.

Do not include hostnames, account codes, or names of the systems or organizations a deployment serves, in code, tests, or text. Test fixtures use neutral names.

## The user guide

`docs/HELP.md` is the user guide: the app's Help page shows it, and the assistant reads it to answer questions about the Studio. A change people will see updates it in the same pull request, and the README too when the change alters what the Studio offers. Keep the guide short and task-first: what a part of the app is for and the few things people do there, in plain words. Details go in `docs/REFERENCE.md` or the topic documents under `docs/`, which the assistant also reads. Name settings exactly as the Settings page labels them. A new document under `docs/` goes in the README's documentation table and in the `STUDIO_DOCS` list in `server/src/chat/tools.ts`, so the assistant can read it.

## Releases

Every version is a tag, and every tag is a GitHub Release whose notes are the descriptions of the pull requests it contains. `CHANGELOG.md` is the same record for every version at once. Both come from one script:

```
node scripts/release-notes.mjs v1.59        # notes for one version
node scripts/release-notes.mjs --changelog  # regenerate CHANGELOG.md
```

To cut a release: merge, tag `vX.Y`, push the tag, publish with `gh release create vX.Y --title vX.Y --notes "$(node scripts/release-notes.mjs vX.Y)"`, and regenerate the change log in a pull request.

## Reporting bugs and security issues

Use the issue templates for bugs and feature requests. Report a vulnerability privately through the repository's Security tab rather than in an issue; see [`SECURITY.md`](SECURITY.md).
