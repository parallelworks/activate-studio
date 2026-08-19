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

## Quick start (a pre-filled training deployment)

`deploy/make_cfd_training_yaml.py` derives a turnkey copy of this
workflow from a site preset file: every deployment value becomes the form
default, scheduler placement included, and the form opens with only the
resource and scheduler groups expanded. `deploy/workflow-cfd-studio.yaml`
is one such copy, generated for a specific site and committed so people
can add it directly from this repository as a remote workflow (repo, the
`main` branch, and that path) rather than each building a record by hand.
Regenerate it after changing this workflow. A launch then needs three
choices:

1. Resource: the cluster to run on.
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
  request this is where it goes, in the site's own syntax
  (`--gres=gpu:<type>:<count>`, or `-l select=1:ngpus=1` as a PBS
  starting point).
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

## Running a class

A room of trainees launching at once hits three limits, all handled by
staging one shared read-only copy in the site's project space and
pointing the form at it (`make_cfd_training_yaml.py --shared-dir DIR`).
Without it every launch pulls the Studio image, pulls the vLLM image
(several GB), and downloads the model (tens of GB for a 31B model). A
path on the resource is read where it sits, so the copies never
multiply; bucket URIs still cache per user. The directory holds
`containers/studio.sif`, `containers/vllm.sif`, `models/<model dir>`,
and the starter tarball, each world-readable with every parent directory
traversable.

What stays per user: the corpus, the index, the run directory, and the
session name, so trainees do not collide and each keeps their own
uploads and chats.

Node capacity is the remaining ceiling. Where the GPU partition is
`OverSubscribe=EXCLUSIVE`, one trainee stack occupies a whole node
whatever it requests, and concurrent launches beyond the free node count
queue rather than fail. Check the partition's free nodes before a session
and, when the class is larger than that, either stagger launches or serve
one model for everyone (Serve a Model in the Job off, Gateway Base URL
pointed at a model already running) so the trainee jobs need no GPU at
all.

Workflow records are per user on ACTIVATE, with no share operation, so
each trainee needs their own copy: `pw workflows create --yaml
<training yaml> --display-name "<name>" <record name>`, run where the
yaml is readable, or adding this workflow from its repository through the
web interface and filling the deployment fields by hand.

## Operating notes

- Artifacts live in the bucket beside each other: the Studio image, the
  vLLM image, and the starter knowledge base tarball. Refreshing any of
  them is an upload; running stacks are unaffected until relaunch.
- The starter bundle is rebuilt by tarring a curated corpus and uploading
  it; keep working files (chat exports, user uploads, generated models) out
  of it so trainees start clean.
- A workflow record of local type executes the platform's stored copy of
  the yaml rather than the repository, so it does not follow a merge.
  After changing the yaml, push it explicitly: `PATCH
  /api/workflows/<name>` with body
  `{"yaml": "<file contents as a string>"}`, then verify the stored copy
  carries your change before launching.
- A model by itself, without the Studio, is a separate workflow: the
  marketplace Ollama GGUF workflow (scheduler placement, Slurm and PBS,
  any GGUF tag from HuggingFace) fits compute clusters, and the
  `ollama-endpoint` and `vllm-endpoint` workflows serve from GPU login
  hosts; both register the model in the platform catalog.
- One stack per session name: a new submission cancels the previous job
  with the same name first. Each submission writes its own output file
  (`job-<timestamp>.out`, with a `job.out` symlink to the current one), so
  the watcher never reads a dead job's state.

## Troubleshooting

- *Rejected with a GRES, select, or "node configuration is not available"
  message*: the site requires an explicit GPU request, or the requested
  shape does not exist in that partition. Put the request in Extra
  Scheduler Directives in the site's own syntax; `sinfo -o "%P %G %D %t"`
  lists which GPU shapes each partition actually has.
- *Long "waiting" phase on first launch*: the model download. Watch the
  run's log; later launches skip it.
- *Session up but chat answers slowly*: normal for a large model on one GPU;
  the tool-activity feed shows progress during grounded answers.
- *Empty knowledge base after launch*: the starter bundle pull failed
  (check the run log for the bucket error); the app still works, and a
  relaunch retries the seed while the directory is empty.
- *"scheduler placement runs the container source only"*: the record or
  form has App Source set to GitHub or Bundle; switch it to Container.
