# ACTIVATE Studio on Compute

`deploy/workflow-compute.yaml` submits one scheduler job that runs the whole
Studio stack on a compute node: a vLLM model server on the node's GPUs, the
Studio container beside it with its gateway pointed at that local model, and
the platform session registrations made from the node itself. The pattern is
the same as running Jupyter on compute: the app comes up when the job runs,
works against a persistent corpus on the site filesystem, and ends with the
walltime, while the session names, corpus, and index survive for the next
launch. The login-node workflow (`deploy/workflow.yaml`) is the alternative
when the app should outlive scheduler jobs.

## Quick start

The form defaults are a complete, tested configuration: the CFD AI Studio
with a Gemma 4 31B serve on one H100, a starter corpus, mission, prompts,
and branding. A first launch needs three choices:

1. Resource: the cluster to run on.
2. Account: your scheduler account (allocation).
3. QOS: your quality of service, where the site requires one.

Everything else can stay as it is. The run waits in the queue, pulls the
container images and model on first use (the model download is the long
step; later launches reuse it), and registers two sessions: the Studio and
the model serve. Open the Studio session when it shows running.

On the first launch the knowledge base directory is empty, so it seeds
itself from the starter bundle: OpenFOAM tutorial cases, tutorials, solver
manuals, papers, and surface geometry models. A knowledge base that already
has content is never touched, so uploads and generated files survive
relaunches.

## What each input group controls

- **Resource and scheduler**: where the job runs and how it is submitted.
  Partition, walltime, and extra directives default to proven H100 values
  and are also backed by in-script fallbacks, so a cleared field cannot
  produce an unschedulable job. Walltime defaults to two hours; the stack
  ends with the job, and relaunching resumes with the same corpus.
- **Containers**: the Studio image, pulled from a bucket and cached on the
  resource. Rebuild with `apptainer build studio.sif deploy/app.def` and
  upload to the bucket to release a new version.
- **Model**: the vLLM serve. Defaults run Gemma 4 31B with its tool-calling
  parsers and chat template on one GPU. `serve: false` skips the local model
  and the Studio uses the platform gateway only. The model directory is
  reused across launches; the first download is tens of GB.
- **Knowledge base**: corpus path, index path, display label, the starter
  bundle, and the mission statement injected into the assistant's system
  prompt (editable later in the app's Settings).
- **App**: name, session name, suggested prompts, icon. Session names are
  per-user on the platform, so two people launching with defaults do not
  collide.

## Operating notes

- Artifacts live in the bucket beside each other: the Studio image, the
  vLLM image, and the starter knowledge base tarball. Refreshing any of
  them is an upload; running stacks are unaffected until relaunch.
- The starter bundle is rebuilt by tarring a curated corpus and uploading
  it; keep working files (chat exports, user uploads, generated models) out
  of it so trainees start clean.
- On activate.hpc.mil, runs execute the platform's stored copy of this
  yaml, which lags the repository. After changing the yaml, push it
  explicitly: `PATCH /api/workflows/activate-studio-compute` with body
  `{"yaml": "<file contents as a string>"}`, then verify the stored copy
  carries your change before launching.
- One stack per session name: a new submission cancels the previous job
  with the same name first. Each submission writes its own output file, so
  the watcher never reads a dead job's state.

## Troubleshooting

- *Rejected with a GRES message*: the site requires an explicit GPU
  request. The scheduler-directives field (and its fallback) carries
  `--gres=...`; adjust it to the node type the site names in the error.
- *Long "waiting" phase on first launch*: the model download. Watch the
  run's log; later launches skip it.
- *Session up but chat answers slowly*: normal for a 31B model on one GPU;
  the tool-activity feed shows progress during grounded answers.
- *Empty knowledge base after launch*: the starter bundle pull failed
  (check the run log for the bucket error); the app still works, and a
  relaunch retries the seed while the directory is empty.
