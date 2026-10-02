# Workflows tab

The Workflows tab offers a set of ACTIVATE workflows chosen for this Studio. Each appears as a tile; opening one shows the workflow's own input form, and Validate or Run submits it to the platform. The assistant sees the same set: `list_workflows` marks those workflows `offeredHere` and lists them first.

The tab appears only when the Studio can reach a platform, through the viewer's own platform key or the deployment's credential. A Studio running anywhere else does not show it.

## Choosing the set

An entry is one of three kinds:

| Entry | Runs |
|---|---|
| `name` | a workflow saved on the account; a viewer without it can add a copy from its tile |
| `marketplace/<slug>` | the marketplace workflow, by slug, with no copy in anyone's account |
| `github.com/<owner>/<repo>[/path][@ref]` | the workflow.yaml in that repository (or directory, or named file), with no copy in anyone's account |

The form for a GitHub entry is read from the repository's raw file, so it shows for public repositories; a private repository still runs, through the platform's connected account.

An administrator picks workflows in Settings, Workflows, from the workflows on the platform account the Studio is deployed under (a site deployment offers from that site's workflows), from the marketplace, or by adding a GitHub repository, which is checked before it is added. Order in the list is the order of the tiles. A name that is no longer on the account is flagged there and shown as an unavailable tile.

At deploy time, the Offered Workflows input (environment variable `STUDIO_WORKFLOWS`, comma-separated names) sets the initial list. A list saved in Settings takes its place; clearing it in Settings returns to the deploy-time list.

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

## Endpoints

| Route | Purpose |
|---|---|
| `GET /api/workflows/collection` | the tiles, as the viewer has them |
| `GET /api/workflows/catalog` | the deployment account's workflows and the marketplace's, for Settings |
| `GET /api/workflows/item/form?w=<entry>` | the converted form and saved configurations |
| `GET /api/workflows/item/icon?w=<entry>` | the tile's icon |
| `POST /api/workflows/item/run?w=<entry>` | `{inputs, dryRun}`; validate or submit |
| `POST /api/workflows/item/install?w=<entry>` | copy the deployment's definition of an account workflow into the viewer's account |
| `GET /api/workflows/runs` | recent runs |
| `GET /api/platform/clusters`, `/partitions?cluster=`, `/buckets` | data for the pickers |
