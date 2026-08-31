---
name: workflow_authoring
description: Draft, validate, visualize, and hand off platform workflow YAML that actually runs
---

Author workflow YAML against the platform's schema, not from memory of what workflows look like. The schema is the authority and validate_workflow consults it directly.

The rules that matter:

1. The document requires a `jobs` map. Job keys are lowercase (`^[a-z0-9_-]{1,255}$`), and every job requires `steps`. Dependencies are `needs: [other_job]` on the job.
2. Each step is either a `run` command or a `uses` subworkflow reference, never both. Legal `uses` values are only: the builtin actions (`parallelworks/checkout`, `parallelworks/scheduler-agent`, `parallelworks/wait-for-agent`, `parallelworks/cancel-jobs`, `parallelworks/update-session`), `marketplace/<slug>[/<version>]`, `workflow/<name>` for a workflow saved in this account, or `github/<owner>/<repo>...`. A bare workflow name is invalid; write `workflow/<name>`.
3. User-facing inputs live at `on.execute.inputs`, and values reach steps as `${{ inputs.<name> }}` in a step's `with:` map. Only interpolation strings, numbers, and the declared input types belong there.
4. `parallelworks/checkout` requires `with: {repo, branch}`; `parallelworks/wait-for-agent` requires `with: {agentId}`.

The working loop:

1. Compose from existing pieces with compose_workflow when the goal is chaining known workflows; it validates its own output and reports schema errors.
2. For hand-written or edited YAML, run validate_workflow (inline yaml or a knowledge base path) and fix every error it reports. Do not write the file, embed its DAG, or offer to run it while errors remain.
3. Write the finished YAML into the knowledge base with write_kb_file, then show it with `![name DAG](/?embed=dag&path=<url-encoded path>)`. The viewer also surfaces schema errors, so a clean embed doubles as confirmation.
4. To make it runnable, the user registers it on the platform (`pw workflows create --yaml <file> <name>`); after that it appears in list_workflows and run_workflow can dry-run it.
