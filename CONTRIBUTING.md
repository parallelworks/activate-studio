# Contributing

Contributions are welcome: bug reports, fixes, new extractors, viewers, documentation. The Studio is licensed under Apache 2.0, and a contribution is accepted under the same license.

## Getting it running

The README's standalone section and [`docs/MACOS.md`](docs/MACOS.md) cover setup. The short version:

```
pnpm install
pnpm build
pnpm start          # http://localhost:4080, over knowledge-base/ beside the code
```

Node 22 or newer and pnpm are required. GUFI is needed only for the indexed search surfaces; everything else runs without it.

## Working in the repository

This is a pnpm workspace: `server/` is the Fastify API and `web/` the React client. Add or upgrade a dependency with pnpm from the repository root, for example `pnpm --filter @activate-studio/web add <package>`, and commit `pnpm-lock.yaml` with it. Running `npm install` inside a package leaves the lockfile stale and CI, which installs with a frozen lockfile, will fail.

New dependency versions must be at least 24 hours old; the workspace enforces this.

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

## Reporting bugs and security issues

Use the issue templates for bugs and feature requests. Report a vulnerability privately through the repository's Security tab rather than in an issue; see [`SECURITY.md`](SECURITY.md).
