# Deploying

Three ways to run the Studio, then how it reaches a model. Settings and branding are in [`CUSTOMIZATION.md`](CUSTOMIZATION.md).

## On ACTIVATE

[`deploy/workflow.yaml`](../deploy/workflow.yaml) deploys the Studio onto a connected resource from the ACTIVATE workflow form. It has two source modes:

| Mode | What it does |
|---|---|
| GitHub (default) | clones this repository and builds on the resource, fetching Node and the embedding model when they are missing |
| Bundle | unpacks a self-contained tarball from `deploy/make_bundle.sh`, pulled from a bucket or pre-staged at `<workdir>/bundle-prestage.tar.gz`, for systems without outbound network |

GUFI is built from source on the resource when cmake is available; without it the Studio still runs, with folder browsing and plain text search. The session method `web` serves the Studio through a platform session that outlives the workflow, `e2e` starts it, checks `/healthz`, and stops it, and `cleanup` stops what an earlier launch left running. The workflow can also run everything, the model server included, as one batch job on a compute node under Slurm or PBS: [`deploy/COMPUTE.md`](../deploy/COMPUTE.md).

On the platform, the assistant gains workflow tools (the account's catalog, a preview of each workflow's steps, dry-run validation, runs on request, and run monitoring) from the pw CLI. They are hidden where the CLI is absent; the knowledge base tools work everywhere.

## In a container

`deploy/app.def` packages the server, the interface, GUFI, and the text extraction tools as one Apptainer image, for container-first sites and for the compute-node workflow. Build it on Linux with Apptainer 1.2 or later:

```
apptainer build studio.sif deploy/app.def
```

Run it with the knowledge base and its index bound in:

```
apptainer run --bind /path/to/folder:/kb --env KB_ROOT=/kb \
  --bind /path/to/index:/kb-index --env INDEX_BASE=/kb-index \
  --env PORT=4080 studio.sif
```

The deploy workflow pulls a prebuilt image from a bucket when one is configured, so most deployments never build one.

## As a plain server

The README's quick start is the whole procedure. Values that differ per deployment go in a gitignored `.env` at the repository root (`deploy/run_endpoint.sh` reads it), starting from `.env.example`. `KB_ROOT` is the folder to work over; without it the Studio uses `/data/knowledge-base` where that exists, and otherwise a sample folder beside the code. `PORT` defaults to 4080. New and changed files are picked up every `SWEEP_INTERVAL_SEC` seconds (300 by default; 0 turns it off).

## Models

The chat works with any OpenAI-compatible endpoint that serves `/models` and streaming `/chat/completions`, with tool calling for the assistant's tools.

- **Anywhere:** set `OPENAI_BASE_URL` to the endpoint's `/v1` address and `OPENAI_API_KEY` to its key. OpenAI, vLLM, the llama.cpp server, and Ollama all work.
- **On ACTIVATE:** with a signed-in pw CLI on the host, nothing needs setting; the Studio uses the CLI's credential for the platform's AI gateway, and every model the account can reach appears in the model list. Otherwise set `PW_API_KEY`. `PW_ALLOCATION` turns on organization-provided models.
- **Image descriptions:** `ADE_VISION_MODEL` names a vision model, which makes images findable by what they show.
