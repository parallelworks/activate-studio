# Workflows tab

The Workflows tab offers a set of ACTIVATE workflows chosen for this Studio. Each appears as a tile; opening one shows the workflow's own input form, and Validate or Run submits it to the platform. The assistant sees the same set: `list_workflows` lists those workflows first, marked `offeredHere`, marketplace and GitHub entries included, and `get_workflow`, `workflow_configs`, and `run_workflow` accept every kind of entry.

The tab appears only when the Studio can reach a platform, through the viewer's own platform key or the deployment's credential. A Studio running anywhere else does not show it.

## Choosing the set

An entry is one of three kinds:

| Entry | Runs |
|---|---|
| `name` | a workflow saved on the account; a viewer without it can add a copy from its tile |
| `marketplace/<slug>` | the marketplace workflow, by slug, with no copy in anyone's account |
| `github.com/<owner>/<repo>[/path][@ref]` | a workflow file in that repository, with no copy in anyone's account |

An administrator picks workflows in Settings, Workflows, from the workflows on the platform account the Studio is deployed under (a site deployment offers from that site's workflows), from the marketplace, or from a GitHub repository. Order in the list is the order of the tiles. A name that is no longer on the account is flagged there and shown as an unavailable tile.

At deploy time, the Offered Workflows input (environment variable `STUDIO_WORKFLOWS`, comma-separated names) sets the initial list. A list saved in Settings takes its place; clearing it in Settings returns to the deploy-time list.

## GitHub workflows

A GitHub entry reaches workflows that are never published to the marketplace, such as the component workflows in a repository of building blocks (a design of experiments, a design explorer) that other workflows call. A directory provides its workflow file in one of two layouts:

| Layout | Entries |
|---|---|
| `<dir>/workflow.yaml` (or `.yml`) | one, the directory or the file |
| `<dir>/yamls/<variant>.yaml` | one per variant file; a directory with a single variant can be named by directory |

`pw workflows run` takes a directory only when it holds a workflow.yaml, so the Studio resolves an entry to its file and runs that, ref included: `github.com/parallelworks/workflows/workflows/doe@canary` runs `github.com/parallelworks/workflows/workflows/doe/yamls/general.yaml@canary`. A directory with several variants is refused until one is named.

In Settings, Workflows, From GitHub takes a repository, a directory, or a file, as an entry or a GitHub link (`https://github.com/<owner>/<repo>/tree/<ref>/<path>` is rewritten to `github.com/<owner>/<repo>/<path>@<ref>`), and lists every workflow file found under it with the title and first paragraph of its directory's README. Ticking one adds it to the set. Tiles take the same title and summary, and the first PNG, JPEG, or WebP in the directory's `thumbnails/` as their icon (SVG is skipped).

The Studio reads repositories through GitHub's public API, which allows 60 requests an hour from one address without a token, and caches each repository's file list for 10 minutes. Set `GITHUB_TOKEN` on a Studio that browses often. Forms show for public repositories only; a private repository still runs, through the platform's connected account.

*Access to account variables:* a workflow can declare `permissions` in its YAML (`'*'` is every account variable). The platform runs it only with `--trust`, after which the grant stays with the repository until `pw workflows permissions revoke` removes it. The tab shows the requested access above Run and Validate, which stay disabled until the viewer ticks the approval; a validation needs it too. The assistant's `run_workflow` returns the requested access without running, and runs with `trust: true` only after the user approves in the conversation.

## Who a run belongs to

Workflow records on the platform are per user, and a run uses the caller's credential. A viewer with their own platform key runs their own copy of each workflow, under their own allocation and permissions. When a viewer lacks a curated workflow, the tile's form offers to add it: the Studio copies the deployment account's definition into the viewer's account with `pw workflows create`. A viewer without a key runs under the deployment's credential.

Only workflows in the set can be run from the tab; any other name is refused with 403.

## The form

The form is the platform's own renderer from `@parallelworks/ui`, fed by the platform's schema conversion, so conditional fields, groups, and defaults behave as they do on the platform. A workflow's saved configurations appear as presets. A few input types read live platform data, and the Studio supplies those:

| Input type | In the Studio |
|---|---|
| `compute-clusters` | a list of the viewer's clusters; submitted as the cluster's `pw://` reference |
| `dynamicPartitionDropdown` | the partitions of the chosen cluster, with free nodes |
| `bucket` | a list of the viewer's buckets |
| `dynamicAccountDropdown`, `dynamicQOSDropdown` | text inputs, since the platform API does not list Slurm accounts or QoS; a preset fills them |
| `editor` | a text area |
| Kubernetes and instance types | text inputs |

Recent runs from the tab are listed below the tiles with their state.

## Sessions

Workflows often open a session: a design explorer, a notebook, a remote desktop. The tab lists the viewer's running sessions above the recent runs, with the run that opened each, and the runs table names each run's session. The list comes from the platform API (`GET /api/sessions`) with the viewer's key, because the CLI's session output leaves out the field that ties a session to its run in older releases.

A session opens in the Studio when the browser can show it in a frame, and otherwise in its own tab; every view has a button for its own tab. Framing works when three things hold:

- the session is served from its own host under the sessions domain (an endpoint session), not under the platform's address (`/me/session/...`), whose pages refuse to be framed;
- the Studio is open in its own tab, not inside the platform's page;
- the Studio's address and the session's are on the same site, which they are when the Studio runs as a session on the same platform.

The platform signs a browser in to each session host separately, with a `SameSite=Lax` cookie that a frame receives only under those conditions. The first visit goes through the platform's login page, which cannot load in a frame, so a session the browser has never opened shows empty or refused until it is opened once in its own tab. The view says so under the frame.

In chat, `list_sessions` and `watch_run` give the assistant each session with the markdown that shows it: `![name](/?embed=session&user=<owner>&name=<session>)` for a session on its own host, or a link for one under the platform's address. When a run launched from chat opens a session, the run watcher adds it to that conversation once it is running.

## Endpoints

| Route | Purpose |
|---|---|
| `GET /api/workflows/collection` | the tiles, as the viewer has them |
| `GET /api/workflows/catalog` | the deployment account's workflows and the marketplace's, for Settings |
| `GET /api/workflows/item/form?w=<entry>` | the converted form, saved configurations, the entry it runs as (`runAs`), and declared `permissions` |
| `GET /api/workflows/item/icon?w=<entry>` | the tile's icon |
| `POST /api/workflows/item/run?w=<entry>` | `{inputs, dryRun, trust}`; validate or submit; `needsTrust` in the reply when the workflow's access was not approved |
| `GET /api/workflows/github/browse?repo=<entry>` | the workflow files in a repository or under one directory of it |
| `GET /api/workflows/github/resolve?w=<entry>` | the file entries a GitHub entry runs as |
| `POST /api/workflows/item/install?w=<entry>` | copy the deployment's definition of an account workflow into the viewer's account |
| `GET /api/workflows/runs` | recent runs |
| `GET /api/sessions` | the viewer's sessions, newest first; `?run=<slug>` keeps one run's |
| `GET /api/sessions/item?user=&name=` | one session |
| `GET /api/platform/clusters`, `/partitions?cluster=`, `/buckets` | data for the pickers |
