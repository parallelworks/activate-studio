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

Server tests cannot reach the network: `server/test/setup.mjs`, loaded before each test file, makes any `fetch` to a non-loopback host fail. A test that needs a platform or GitHub answer replaces `globalThis.fetch` with a stub.

## Pull requests

Open the pull request from a branch on your fork. Keep each one to one change.

The description matters more than usual here. The release notes and `CHANGELOG.md` are generated from pull request descriptions, so write it as the change note a reader will see: what was wrong or missing, what changed, and how you verified it. The title becomes the squashed commit's subject.

Pull requests are squash-merged and need two approving reviews from maintainers, with review threads resolved. First-time contributors' CI runs start after a maintainer approves them.

Commit messages must not carry automated-assistant attribution trailers; the organization's rules reject them at merge.

Do not include hostnames, account codes, or names of the systems or organizations a deployment serves, in code, tests, or text. Test fixtures use neutral names.

## The user guide

`docs/HELP.md` is the user guide: the app's Help page shows it, and the assistant reads it to answer questions about the Studio. A change people will see updates it in the same pull request, and the README too when the change alters what the Studio offers. Keep the guide short and task-first: what a part of the app is for and the few things people do there, in plain words. Details go in `docs/REFERENCE.md` or the topic documents under `docs/`, which the assistant also reads. Name settings exactly as the Settings page labels them. A new document under `docs/` goes in the README's documentation table and in the `STUDIO_DOCS` list in `server/src/chat/tools.ts`, so the assistant can read it.

## Keeping up with the UI package

The Studio's interface is built from `@parallelworks/ui` (the [foundation](https://github.com/parallelworks/foundation) repository), which releases often. A daily workflow, `foundation-watch`, keeps one issue labeled `foundation-update` open while the lockfile is behind npm, with the changelog in between; it closes the issue once the Studio catches up. To take a release: bump with pnpm from the root, run `UPDATE_THEME_DEFAULTS=1 npx vitest run test/themeDefaults.test.ts` in `web` if the theme test fails, check every view in light and dark against the previous release, and adopt what the changelog adds that the Studio builds itself.

## Releases

Every version is a tag, and every tag is a GitHub Release whose notes are the descriptions of the pull requests it contains. `CHANGELOG.md` is the same record for every version at once. Both come from one script:

```
node scripts/release-notes.mjs v1.59                       # notes for one version
node scripts/release-notes.mjs --changelog                 # regenerate CHANGELOG.md
node scripts/release-notes.mjs --changelog --next v1.60    # with the untagged commits as v1.60
```

To cut a release: merge; regenerate the change log with `--next vX.Y` and merge it in a pull request titled "Change log through vX.Y"; tag that commit `vX.Y` and push the tag; publish with `gh release create vX.Y --title vX.Y --notes "$(node scripts/release-notes.mjs vX.Y)"`. The change log comes before the tag so the version's own build carries its notes, which the app shows after an update and under What's new.

The macOS app is built only for the releases that should reach Mac users, since releases are frequent: `gh workflow run mac-app.yml -f release=vX.Y` (or the Actions tab) builds that tag on a Mac, signs its update manifest, attaches the app to the release, and puts the same files in the `macos` release, the fixed address the installer and the docs use. Installed apps update to the newest release that has the app, and their update notice lists the changes of the releases in between.

### Signing and notarizing the macOS app

With the secrets and the variable below, the release build also signs the app with the Developer ID certificate, notarizes its disk image, and staples the tickets (the `sign` job in `mac-app.yml`), so the app opens without the Open Anyway step. That job runs only the `deploy/mac` scripts from the release's tag, on the app the build job made, and it is the only job that sees these secrets. Without `APPLE_TEAM_ID`, or with any secret missing, it signs nothing and the release carries the app signed ad hoc. On every pull request, the `hardened` job signs a copy of the app the same way with a throwaway self-signed identity and runs it, which shows a file left unsigned or entitlements other than `deploy/mac/entitlements` names. macOS does not enforce the hardened runtime for a signature without Apple's certificate chain, so whether each program has the entitlements it needs shows only when the `sign` job runs the app signed with the certificate.

| Name | Kind | Value |
|---|---|---|
| `MACOS_SIGNING_P12` | secret | the Developer ID Application certificate with its private key, a .p12 file in base64 |
| `MACOS_SIGNING_P12_PASSWORD` | secret | the .p12's password |
| `NOTARY_KEY_P8` | secret | the text of an App Store Connect API key's .p8 file |
| `NOTARY_KEY_ID` | secret | that key's ID |
| `NOTARY_ISSUER_ID` | secret | the issuer ID shown with the account's API keys |
| `APPLE_TEAM_ID` | variable | the Apple Developer team ID; the job signs only when it is set |

*The certificate:* the team's Account Holder creates a Developer ID Application certificate in the Apple Developer account (Certificates, Identifiers & Profiles), from a certificate signing request made with Keychain Access (Certificate Assistant, Request a Certificate From a Certificate Authority, saved to disk); Xcode's Settings, Accounts, Manage Certificates does both in one step. On that Mac, Keychain Access then lists it under My Certificates with its private key: select it, choose File, Export Items, and save a .p12 with a password.

```
base64 -i DeveloperID.p12 | tr -d '\n' | gh secret set MACOS_SIGNING_P12 -R parallelworks/activate-studio
gh secret set MACOS_SIGNING_P12_PASSWORD -R parallelworks/activate-studio
```

*The notary key:* in App Store Connect, Users and Access, Integrations, App Store Connect API, generate a team key with Developer access. Its .p8 file downloads once; the key ID is in the key's row and the issuer ID above the list.

```
gh secret set NOTARY_KEY_P8 -R parallelworks/activate-studio < AuthKey_KEYID.p8
gh secret set NOTARY_KEY_ID -R parallelworks/activate-studio
gh secret set NOTARY_ISSUER_ID -R parallelworks/activate-studio
```

*The team ID:* the ten characters on the Apple Developer account's Membership details page.

```
gh variable set APPLE_TEAM_ID -R parallelworks/activate-studio
```

Given no value, `gh secret set` asks for it without showing it. The job picks the identity in the .p12 by its kind and team, so a renewed certificate (each lasts five years) needs only the two certificate secrets replaced; apps signed with the old one keep opening, since every signature carries a secure timestamp.

## Reporting bugs and security issues

Use the issue templates for bugs and feature requests. Report a vulnerability privately through the repository's Security tab rather than in an issue; see [`SECURITY.md`](SECURITY.md).
