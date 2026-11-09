# Deploying

Three ways to run the Studio, how people sign in to a shared one, then how it reaches a model. Settings and branding are in [`CUSTOMIZATION.md`](CUSTOMIZATION.md).

## On ACTIVATE

[`deploy/workflow.yaml`](../deploy/workflow.yaml) deploys the Studio onto a connected resource from the ACTIVATE workflow form. Its GitHub and Bundle modes run the Studio's Linux app, `activate-studio-linux-x86_64.tar.gz`, the same file the Linux release ships: the Go server, the built interface, GUFI, tesseract, and the ONNX runtime with the search by meaning model. Nothing is built on the resource, and it needs no Node or Python.

| Mode | What it does |
|---|---|
| GitHub release (default) | downloads the Linux app of the release named in Release (a tag such as `v1.190`; `main` means the current Stable release) and checks it against its `.sha256` |
| Bundle | the same file from a bucket, or pre-staged at `<workdir>/bundle-prestage.tar.gz`, for systems without outbound network. `deploy/make_bundle.sh` downloads a release's file and checks it, ready to upload |
| Container | runs the Apptainer image built from the same Linux app (`deploy/app-go.def`, below) |

A relaunch from the same release or bundle file keeps the app in place unless Load Latest Bundle is set. The index, settings and credentials live in the Index Directory, by default `<workdir>/data/index`. A deployment that kept them in `<workdir>/app/index` under v1.188 or earlier has them moved there on its first launch of the Go version, so its index, labels, conversations and settings carry over. With Index on Launch set, the workflow starts a full index once the server answers, and the Studio serves while it builds.

The session method `web` serves the Studio through a platform session that outlives the workflow, `e2e` starts it, checks `/healthz`, and stops it, and `cleanup` stops what an earlier launch left running. In `web` mode a watchdog checks the server every two minutes. A server that stops answering is restarted on the same port, so the session keeps its address; if the session's endpoint process is gone, the watchdog relaunches the whole deployment. The server writes `app.log` in the workdir; a launch moves the previous server's log to `app.log.prev`. The workflow can also run everything, the model server included, as one batch job on a compute node under Slurm or PBS: [`deploy/COMPUTE.md`](../deploy/COMPUTE.md).

On the platform, the assistant gains workflow tools (the account's catalog, a preview of each workflow's steps, dry-run validation, runs on request, and run monitoring) from the pw CLI. They are hidden where the CLI is absent; the knowledge base tools work everywhere.

## In a container

`deploy/app-go.def` packages the Linux app (the Go server, the interface, GUFI, tesseract and the search by meaning model) as one Apptainer image, for container-first sites and for the compute-node workflow. Each release publishes it to ghcr.io from that release's Linux app (`.github/workflows/container-go.yml`), tagged with the version, and `latest` for the newest Stable release:

```
apptainer pull studio.sif oras://ghcr.io/parallelworks/activate-studio:latest
```

To build one, unpack a release's `activate-studio-linux-x86_64.tar.gz` so that `.container/activate-studio` is its top folder, then from the repository root, on Linux with Apptainer 1.2 or later:

```
apptainer build studio.sif deploy/app-go.def
```

Run it with the knowledge base and a writable data folder bound in. The index, settings and credentials go in `index` under the data folder:

```
apptainer run --bind /path/to/folder:/kb --env KB_ROOT=/kb \
  --bind /path/to/data:/data --env DATA_DIR=/data \
  --env PORT=4080 studio.sif
```

`INDEX_ON_START=1` starts a full index once the server answers. `deploy/app.def` builds the Node version's image, which releases no longer publish.

The deploy workflow pulls a prebuilt image from a bucket when one is configured, so most deployments never build one.

## As a plain server

The README's quick start is the whole procedure. Values that differ per deployment go in a gitignored `.env` at the repository root (`deploy/run_endpoint.sh` reads it), starting from `.env.example`. `KB_ROOT` is the folder to work over; without it the Studio uses `/data/knowledge-base` where that exists, and otherwise a sample folder beside the code. `PORT` defaults to 4080. New and changed files are picked up every `SWEEP_INTERVAL_SEC` seconds (300 by default; 0 turns it off).

## Sign-in

A Studio serves one person unless it is told how to tell people apart. Two ways, which can be used together:

- **Behind the ACTIVATE session proxy:** set `AUTH_JWKS_URL` to `https://<platform host>/api/platform/keys`. The proxy forwards a short-lived JWT naming the user with each request ([`CUSTOMIZATION.md`](CUSTOMIZATION.md)).
- **Through an OpenID Connect provider:** ACTIVATE (a feature preview) or any provider with a discovery document, such as Keycloak, Entra ID or Okta. People sign in once and the Studio keeps a session for them. Sign-in is handled by the Go server, which starts Node behind it (`server-go`, `STUDIO_NODE_SPAWN=1`; [`GO-PORT.md`](GO-PORT.md)); the Node server started on its own does not offer it.

A request that carries a valid proxy JWT is that JWT's user; otherwise the sign-in session decides. Either way the Studio runs in shared mode: setup, the pw CLI's sign-in and other deployment-level controls are closed, and each person brings their own model and platform key under Settings, Model access.

| Variable | Meaning |
|---|---|
| `OIDC_ISSUER` | the provider's issuer; discovery is read from `<issuer>/.well-known/openid-configuration`. On ACTIVATE, `https://<platform host>/api/oidc` |
| `OIDC_CLIENT_ID` | the registered application's client ID |
| `OIDC_CLIENT_SECRET` | its secret; without one the Studio signs in as a public client, with PKCE alone |
| `OIDC_REDIRECT_URI` | the registered redirect URI, `https://<studio address>/api/auth/callback`; unset, it is built from each request's address |
| `OIDC_POST_LOGOUT_REDIRECT_URI` | where the provider sends the browser after sign-out; it must be registered with the provider |
| `OIDC_SCOPES` | default `openid profile email groups`; add `offline_access` for refresh tokens |
| `OIDC_USERNAME_CLAIM` | the claim shown as the username and used for conversation ownership (default `preferred_username`, then `email`, then `sub`) |
| `AUTH_ACCOUNT_CLAIM`, `AUTH_UID_CLAIM`, `AUTH_GID_CLAIM` | claims carrying a host account name and a numeric uid and gid, where the provider releases them (for example `uidNumber` and `gidNumber` from a directory). Recorded on the signed-in user for later use; nothing acts on them yet |
| `OIDC_END_SESSION` | `1`: sign-out also ends the provider's session (its `end_session_endpoint`). By default sign-out ends the Studio's session only |
| `OIDC_SESSION_MAX_AGE_SEC` | how long a sign-in lasts, default 43200 (12 hours) |
| `OIDC_REQUIRED` or `AUTH_REQUIRED` | `1`: API requests need a signed-in user, and a page load without one goes to sign-in. `/healthz`, the app's script and style files, and a mission worker's token on `/api/mcp` stay open |

The username from the provider is not assumed to be an account on any host. ACTIVATE releases `sub`, `name`, `preferred_username`, `email` and `groups`, and no uid or gid; its username can differ from a person's login on a cluster. Name `AUTH_ACCOUNT_CLAIM` only for a provider whose claim is the host account.

The provider's tokens stay on the server and identify the person; they are never used to call the platform or a model. Running workflows, sessions and platform models as the person still needs their own platform key under Model access. Sessions are kept across restarts, encrypted, in `INDEX_BASE/oidc-sessions.enc`; the key is `INDEX_BASE/.oidc-session-key`, written 0600, and deleting it ends every sign-in. Signing in again does not ask for a password while the provider's own session lasts. With `offline_access`, the Studio refreshes the provider's tokens as they expire, and a person the provider no longer admits is signed out within the access token's lifetime (an hour on ACTIVATE).

### Registering the Studio with ACTIVATE

An organization administrator registers the application once; this changes platform settings and is not done by the Studio.

1. Pick the Studio's address. The application is registered for one exact redirect URI, so the address has to be fixed: reserve a subdomain (`pw subdomains reserve <label>`) and serve the Studio's session on it, giving `https://<label>.<sessions domain>`.
2. Turn on the *OIDC Applications* preview in your account settings, open Organization, Settings, Applications, and add a **web** application with redirect URI `https://<label>.<sessions domain>/api/auth/callback` and post-logout redirect URI `https://<label>.<sessions domain>/`. Allow the scopes `openid`, `profile`, `email` and `groups`, and `offline_access` if sessions should refresh. Create a client secret; it is shown once. The same through the API:

   ```bash
   pw api -X POST organizations/<org>/oidc/apps --input - <<'EOF'
   {"name": "Studio", "kind": "web", "consent": "none", "subjectType": "username",
    "scopes": ["openid", "profile", "email", "groups", "offline_access"],
    "redirectUris": ["https://<label>.<sessions domain>/api/auth/callback"],
    "postLogoutRedirectUris": ["https://<label>.<sessions domain>/"]}
   EOF
   pw api -X POST organizations/<org>/oidc/apps/<clientId>/secrets
   ```

   `subjectType` `username` makes `sub` name the user, as the session proxy's JWT does, so a person keeps the same conversations and keys whichever way they arrive; check the subject the footer's identity panel shows after the first sign-in. **Allowed groups** restricts who may sign in, and **Require two-step verification** admits only sessions that completed it.
3. Start the Studio with:

   ```
   OIDC_ISSUER=https://<platform host>/api/oidc
   OIDC_CLIENT_ID=<clientId>
   OIDC_CLIENT_SECRET=<secret>
   OIDC_REDIRECT_URI=https://<label>.<sessions domain>/api/auth/callback
   OIDC_POST_LOGOUT_REDIRECT_URI=https://<label>.<sessions domain>/
   OIDC_SCOPES="openid profile email groups offline_access"
   ```

4. Open the Studio **in its own tab**, at `https://<label>.<sessions domain>/`. Sign-in does not complete inside the platform's embedded session view; there the footer's **Sign in** opens a new tab.

## Models

The chat works with any OpenAI-compatible endpoint that serves `/models` and streaming `/chat/completions`, with tool calling for the assistant's tools.

- **Anywhere:** set `OPENAI_BASE_URL` to the endpoint's `/v1` address and `OPENAI_API_KEY` to its key. OpenAI, vLLM, the llama.cpp server, and Ollama all work.
- **A site's AI portal:** add it per person under Settings, Model access, OpenAI-compatible endpoint, with its base URL (usually ending in `/v1`) and key. **Advanced** there takes a portal that wants the key in another header, extra headers, a query string, or typed model names when it does not serve `/models`; **Test** says whether the chosen model calls tools.
- **On ACTIVATE:** with a signed-in pw CLI on the host, nothing needs setting; the Studio uses the CLI's credential for the platform's AI gateway, and every model the account can reach appears in the model list. Otherwise set `PW_API_KEY`. The CLI's current context is used (`PW_CONTEXT` names another); on a single-user install, Settings, Model access lists the contexts, picks one, and signs the CLI in to another platform with its device code. `PW_ALLOCATION` turns on organization-provided models.
- **Image descriptions:** `ADE_VISION_MODEL` names a vision model, which makes images findable by what they show.
