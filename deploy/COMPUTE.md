# ACTIVATE Studio on Compute

The Studio workflow (`deploy/workflow.yaml`) has two placements, chosen by
the "Submit as a Scheduler Job" toggle on the form.

Off, the app runs on the login node and outlives the workflow; this is the
long-lived deployment path and supports all three app sources (container,
GitHub, bundle).

On, the workflow submits one batch job that runs the whole stack on a
compute node: optionally a vLLM model server on the node's GPUs, the Studio
container beside it with its gateway pointed at that local model, and the
platform session registrations made from the node itself. The pattern is
the same as running Jupyter on compute: the app comes up when the job runs,
works against a persistent corpus on the site filesystem, and ends with the
walltime, while the session names, corpus, and index survive for the next
launch. Scheduler placement runs the container source only, since compute
nodes should not build software.

The submission is written in the resource's scheduler dialect, detected
from the resource definition: Slurm (`sbatch`/`squeue`/`scancel`, `#SBATCH`
headers) or PBS (`qsub`/`qstat`/`qdel`, `#PBS` headers, job name capped at
15 characters). The form shows the matching settings group only; Slurm
account, partition, and QOS are populated from the resource. Slurm
placement is proven in production runs; PBS follows the marketplace script
submitter's mechanics and has passed simulation but not yet a live PBS
cluster, so treat the first PBS launch as a shakedown.

`deploy/workflow-compute.yaml` is the superseded standalone version of the
scheduler placement, kept only for platform records that still reference
it.

## Quick start (CFD training on makau)

Two ways to get the pre-filled training package, the CFD AI Studio with a
Gemma 4 31B serve, a starter corpus, mission, prompts, and branding, with
scheduler placement already on. On activate.hpc.mil the `cfd-studio`
record is the turnkey copy: every training value is the form default,
generated from this workflow by `deploy/make_cfd_training_yaml.py` (rerun
it and PATCH the record's stored yaml after workflow changes). On any
platform serving the plain workflow, the `configurations` block does the
same through a per-resource preset that fires when makau is selected. A
first launch needs three choices:

1. Resource: makau.
2. Account: your scheduler account (allocation).
3. QOS: your quality of service.

Everything else can stay as it is. The run waits in the queue, pulls the
container images and model on first use (the model download is the long
step; later launches reuse it), and registers two sessions: the Studio and
the model serve. Open the Studio session when it shows running.

On the first launch the knowledge base directory is empty, so it seeds
itself from the starter bundle: OpenFOAM tutorial cases, tutorials, solver
manuals, papers, and surface geometry models. A knowledge base that already
has content is never touched, so uploads and generated files survive
relaunches.

## What the scheduler-placement inputs control

- **Slurm Job / PBS Job**: submission settings in the resource's dialect.
  Walltime is backed by a two-hour in-script fallback; empty account,
  partition, QOS, and queue mean the flag is omitted and the cluster
  default applies. Extra directives are semicolon-separated lines without
  the `#SBATCH`/`#PBS` prefix; on systems that require an explicit GPU
  request this is where it goes (`--gres=gpu:h100_sxm5:4` on makau,
  `-l select=1:ngpus=1` as the PBS starting point).
- **App & Bundle**: source must be Container. The image comes from the
  bucket URI or path in Container Image and is cached at
  `<workdir>/containers/studio.sif` on the shared filesystem so the compute
  node sees it; delete the cached file to force a fresh pull. Rebuild with
  `apptainer build studio.sif deploy/app.def` and upload to the bucket to
  release a new version.
- **Models**: the vLLM serve. Serve a Model in the Job starts vLLM on the
  job's GPUs and points the Studio at it; off, the Studio uses the gateway
  settings. Tool-call and reasoning parsers are model-specific (see the
  vLLM Arguments tooltip). The model directory is reused across launches;
  the first download is tens of GB. The multi-user posture settings apply
  in this placement too.
- **Knowledge Base**: corpus path, index path, display label, the starter
  bundle, and the mission statement injected into the assistant's system
  prompt (editable later in the app's Settings).
- **Session**: the session name is the stable URL across relaunches. The
  web method deploys; the e2e method submits, waits for the stack to come
  up, then cancels the job and reports pass or fail; the cleanup method
  cancels any job with this session's name and exits.

## Operating notes

- Artifacts live in the bucket beside each other: the Studio image, the
  vLLM image, and the starter knowledge base tarball. Refreshing any of
  them is an upload; running stacks are unaffected until relaunch.
- The starter bundle is rebuilt by tarring a curated corpus and uploading
  it; keep working files (chat exports, user uploads, generated models) out
  of it so trainees start clean.
- On activate.hpc.mil, runs execute the platform's stored copy of this
  yaml, which lags the repository. After changing the yaml, push it
  explicitly: `PATCH /api/workflows/<name>` with body
  `{"yaml": "<file contents as a string>"}`, then verify the stored copy
  carries your change before launching.
- A model by itself, without the Studio, is a separate workflow: the
  marketplace Ollama GGUF workflow (scheduler placement, Slurm and PBS,
  any GGUF tag from HuggingFace) fits compute clusters like makau, and
  the `ollama-endpoint` and `vllm-endpoint` records serve from GPU login
  hosts and register the model in the platform catalog.
- One stack per session name: a new submission cancels the previous job
  with the same name first. Each submission writes its own output file
  (`job-<timestamp>.out`, with a `job.out` symlink to the current one), so
  the watcher never reads a dead job's state.

## Troubleshooting

- *Rejected with a GRES, select, or "node configuration is not available"
  message*: the site requires an explicit GPU request, or the requested
  shape does not exist in that partition. Put the request in Extra
  Scheduler Directives in the site's own syntax. On makau the AIML
  partition has quad `h100_sxm5:4` nodes only; the single-GPU
  `h100_nvl:1` nodes are in the standard, debug, background, and high
  partitions.
- *Long "waiting" phase on first launch*: the model download. Watch the
  run's log; later launches skip it.
- *Session up but chat answers slowly*: normal for a 31B model on one GPU;
  the tool-activity feed shows progress during grounded answers.
- *Empty knowledge base after launch*: the starter bundle pull failed
  (check the run log for the bucket error); the app still works, and a
  relaunch retries the seed while the directory is empty.
- *"scheduler placement runs the container source only"*: the record or
  form has App Source set to GitHub or Bundle; switch it to Container.
