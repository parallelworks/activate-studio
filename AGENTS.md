# AGENTS.md

## Commit conventions

Use [Conventional Commits](https://www.conventionalcommits.org/) for all commit messages:

```
<type>(<optional scope>): <description>
```

Common types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `perf`.

Examples:

- `feat(api): add pool status endpoint`
- `fix: handle missing index without blanking the app`
- `docs: document local development setup`

## Pull requests, CI and releases

Several sessions work in this repository at once, and each pull request costs a full CI run (the required checks take 8 to 20 minutes). Most of a day's waiting is CI run again for changes that had already passed it, so:

*One pull request per area per round.* Collect the fixes you have ready for one area (a view, a package, a script) on one branch, one commit per concern, and open one pull request for them. The change log and release notes are generated from pull request descriptions, so the description covers every concern, each in its own short paragraph. While it waits for review or CI, push further fixes for that area to the same branch; a second pull request on the same files conflicts with the first and runs CI twice.

*Rebase only on a conflict.* The ruleset does not require a branch to be up to date with main, and a rebase or a merge of main into a green branch starts every check again. Update a branch only when GitHub reports a conflict, or when the change depends on something that merged since. To rerun a check that failed for a known flake, rerun only the failed jobs (`gh run rerun ID --failed`).

*Documentation in the same pull request, in its own place.* A change people will see updates `docs/HELP.md` (and `docs/REFERENCE.md` or the topic document) in the same pull request. Most pull requests touch `docs/HELP.md`, whose paragraphs are single long lines, so two edits to one paragraph always conflict. Edit only the paragraph about your change; never re-wrap, reorder or retitle the paragraphs around it; put a new topic in a paragraph or under a heading of its own. Leave `CHANGELOG.md` to the release.

*Who releases.* One session at a time owns releases, named by whoever coordinates the day's work; other sessions merge their pull requests and go on working, without waiting for main's CI or for a release build. The owner releases once per round: once the round's pull requests have merged, open the change log pull request on main's tip, merge nothing else until it has passed and merged, then tag. A change log pull request merged while up to date with main has the same tree as the commit it becomes, so its passed checks are that commit's and the tag can go out at once; a merge in between makes main's own run the one to wait for. Publish a Preview with `deploy/release_preview.sh`.
