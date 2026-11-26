# Deploying

Three ways to run the Studio, how people sign in to a shared one and what its administrators set, then how it reaches a model. Settings and branding are in [`CUSTOMIZATION.md`](CUSTOMIZATION.md).

## On ACTIVATE

[`deploy/workflow.yaml`](../deploy/workflow.yaml) deploys the Studio onto a connected resource from the ACTIVATE workflow form. Its GitHub and Bundle modes run the Studio's Linux app, `activate-studio-linux-x86_64.tar.gz`, the same file the Linux release ships: the Go server, the built interface, GUFI, tesseract, and the ONNX runtime with the search by meaning model. Nothing is built on the resource, and it needs no Node or Python.

| Mode | What it does |
|---|---|
| GitHub release (default) | downloads the Linux app of the release named in Release (a tag such as `v1.190`; `main` means the current Stable release) and checks it against its `.sha256` |
| Bundle | the same file from a bucket, or pre-staged at `<workdir>/bundle-prestage.tar.gz`, for systems without outbound network. `deploy/make_bundle.sh` downloads a release's file and checks it, ready to upload |
| Container | runs the Apptainer image built from the same Linux app (`deploy/app-go.def`, below) |

A relaunch from the same release or bundle file keeps the app in place unless Load Latest Bundle is set.

A deployment of a GitHub release updates itself from Settings, General, Updates, as the Linux app does: the same check, the signed and checksummed download, Roll back, and the Stable or Preview channel. Only the person who launched the workflow sees and installs updates (`STUDIO_UPDATE_OWNERS`, set from the platform's `PW_USER`, compared with the signed-in username); to everyone else the Studio says it is updated by its deployment, as before. A deployment without sign-in (the `single` posture) is one person's, and that person updates it. The workflow runs the server under a run loop, `studio-serve.sh` in the workdir: after an install the server exits with status 76 and the loop starts the new copy at once, so the page reloads onto it within seconds and the session keeps its address. The previous copy is kept as `.payload.previous`, and a new version that stops three times within two minutes of starting is replaced by it, as on the desktop. A relaunch that follows Stable keeps a copy updated this way while it is not older than the Stable release; a relaunch pinned to a release tag keeps it while the tag is unchanged, and Load Latest Bundle replaces it. A bundle from a bucket and a container deployment do not update themselves; they are updated by relaunching with a new bundle or image. The index, settings and credentials live in the Index Directory, by default `<workdir>/data/index`. A deployment that kept them in `<workdir>/app/index` under v1.188 or earlier has them moved there on its first launch of the Go version, so its index, labels, conversations and settings carry over. With Index on Launch set, the workflow starts a full index once the server answers, and the Studio serves while it builds.

The session method `web` serves the Studio through a platform session that outlives the workflow, `e2e` starts it, checks `/healthz`, and stops it, and `cleanup` stops what an earlier launch left running. In `web` mode a watchdog checks the server every two minutes, and again 30 seconds later before it acts, so a restart after an update is not taken for a failure. A server that stops answering is restarted on the same port, so the session keeps its address; if the session's endpoint process is gone, the watchdog relaunches the whole deployment. The server writes `app.log` in the workdir; a launch moves the previous server's log to `app.log.prev`. The workflow can also run everything, the model server included, as one batch job on a compute node under Slurm or PBS: [`deploy/COMPUTE.md`](../deploy/COMPUTE.md).

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
- **Through an OpenID Connect provider:** ACTIVATE (a feature preview) or any provider with a discovery document, such as Keycloak, Entra ID or Okta. People sign in once and the Studio keeps a session for them. The releases and the deployment workflow run the Go server, which handles sign-in; the Node server a clone starts does not.

A request that carries a valid proxy JWT is that JWT's user; otherwise the sign-in session decides. Either way the Studio runs in shared mode: setup, the pw CLI's sign-in and other deployment-level controls are closed, and each person brings their own model and platform key under Settings, Accounts.

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
| `STUDIO_ADMINS` | who changes the main library and the Studio's settings on a Studio with sign-in, besides the deployment's owner (`PW_USER`): user names, ids, e-mail addresses or `group:NAME`, separated by commas or spaces. Everyone else reads it only |
| `REQUIRE_PERSONAL_KEY` | `1`: chat, models and **Suggest labels…** refuse the administrator's endpoint and the model served beside the Studio, so each person uses their own sign-in or key; a person's own bucket library describes its images with theirs. `DISABLE_PERSONAL_KEYS=1` is its opposite, and wins when both are set |
| `CHAT_SHARED_HISTORY` | `1`: everyone signed in can open and read everyone's chats, under **All chats**. Off by default with sign-in, and an administrator's choice in Settings, General, AI features comes first. Answers can quote files from people's own libraries |
| `OIDC_REQUIRED` or `AUTH_REQUIRED` | `1`: API requests need a signed-in user, and a page load without one goes to sign-in. `/healthz`, the app's script, style and icon files, the brand image (`APP_ICON`, `APP_FAVICON`), and a mission worker's token on `/api/mcp` stay open |

**The strict posture** is `AUTH_REQUIRED=1` and `REQUIRE_PERSONAL_KEY=1`: every request is a signed-in person's, and each person's chat runs on their own credential. The deployment workflow's **Multi-user, personal keys required** sets both and takes the platform host from the form. With it, name the administrators in `STUDIO_ADMINS`, list the bucket endpoints below, and give the Studio a fixed address. The form's **Sign-in and Sharing** group sets the first two as **Administrators** and **Bucket Endpoints**, `CHAT_SHARED_HISTORY=1` as **Shared Chat History**, and sign-in through ACTIVATE as below; **Public Studio URL**, under App & Bundle, is the fixed address. Who can see what is in [`MULTI-USER.md`](MULTI-USER.md#what-administrators-set-and-see).

The username from the provider is not assumed to be an account on any host. ACTIVATE releases `sub`, `name`, `preferred_username`, `email` and `groups`, and no uid or gid; its username can differ from a person's login on a cluster. Name `AUTH_ACCOUNT_CLAIM` only for a provider whose claim is the host account.

The provider's tokens stay on the server and identify the person; they are never used to call the platform or a model. Running workflows, sessions and platform models as the person still needs their own platform key under Settings, Accounts. Sessions are kept across restarts, encrypted, in `INDEX_BASE/oidc-sessions.enc`; the key is `INDEX_BASE/.oidc-session-key`, written 0600, and deleting it ends every sign-in. Signing in again does not ask for a password while the provider's own session lasts. With `offline_access`, the Studio refreshes the provider's tokens as they expire, and a person the provider no longer admits is signed out within the access token's lifetime (an hour on ACTIVATE).

### ACTIVATE buckets on a Studio people sign in to

Each person can add their own ACTIVATE buckets as libraries, with nothing to set per person (Preview; [`LIBRARIES.md`](LIBRARIES.md#activate-buckets)). It is on when:

- the gateway is the platform's (`PW_GATEWAY_URL` or `OPENAI_BASE_URL`, such as `https://platform.example.com/api/openai/v1`), a Government platform the same as a commercial one;
- the `pw` CLI is on the host (`PW_CLI` names another);
- GUFI has `gufi_trace2index`, which the packages ship.

Each person signs in to ACTIVATE under Settings, Accounts (the device code, or an API key of their own) and then chooses their buckets in **Add a library**. Without one the menu says so; the deployment's credential is never used for it, with or without `REQUIRE_PERSONAL_KEY=1`. A person who keeps their sign-in has their buckets listed on schedule while they are away; one who does not has them listed when they open the Studio.

| Variable | Meaning |
|---|---|
| `AWS_USE_FIPS_ENDPOINT` | `false`: ask the regular S3 endpoints. By default FIPS endpoints are asked in GovCloud and in the US and Canada regions, as the pw CLI asks them |
| `AWS_ENDPOINT_URL_S3`, `AWS_ENDPOINT_URL` | an address every S3 bucket is asked at, such as one VPC endpoint. One that is not an address is an error in the log as the Studio starts, and AWS buckets are not read until it is corrected |
| `STUDIO_BUCKET_ENDPOINTS` | the only hosts, separated by commas, that people may choose as a bucket's endpoint, such as `bucket.vpce-0example.s3.us-gov-west-1.vpce.amazonaws.com` or `s3.storage.example.com:9443`. A host is taken at the port it is listed with, 443 where none is given. Without the list nobody chooses one. Where sign-in is off, the store's own private endpoints (S3 VPC endpoints, Google Private Service Connect) are taken besides |
| `STUDIO_SELF_URL` | the Studio's own address (`http://studio.example.com`). Set it, or `OIDC_REDIRECT_URI`, where the Studio is served over plain http through a proxy that rewrites `Host` and sets no `X-Forwarded-Host`: without it a download or picture of a file in an ACTIVATE bucket looks like another site's there and is refused (403), and so is adding, changing or rebuilding a library if the proxy drops the page's `X-Studio-Request` header |
| `HTTPS_PROXY`, `NO_PROXY` | as for the Studio's other outbound requests; the pw CLI is run with them too. Put the stores' hosts in `NO_PROXY` (`.amazonaws.com`) if they are reached without it |
| `SSL_CERT_FILE` | a bundle with the network's own certificate authority, for the Studio and the pw CLI |

The host asks the platform and the store itself, so both have to be reachable from it: directly, through the proxy, or through a VPC endpoint. When one cannot be, the person is told so and the Studio's log says why. The pw CLI is run without the host's own cloud credentials (`AWS_*`, `GOOGLE_*`, `AZURE_*`, `CLOUDSDK_*`, and instance metadata on AWS). The indexes are in `INDEX_BASE/users`, about what the same number of files would take.

### Links that last

Copy link makes addresses that start with the Studio's own, so they stop working when it changes. The address is the first of these that applies:

1. `STUDIO_SELF_URL`, where it is https and not loopback (the deployment form's **Public Studio URL**).
2. `OIDC_REDIRECT_URI` without `/api/auth/callback`.
3. The address the page is open at.

Set the first to the Studio's fixed subdomain (`pw subdomains reserve <label>` gives one), or launch again under the same session name, which keeps the subdomain. The link log in `INDEX_BASE/link-log`, one file a month, has who made and opened links and when, with the ID of the item and no path; keep it with the audit logs, since nothing removes it. `INDEX_BASE/links.json` maps an ID to its path and is as sensitive as a file listing. See [`REFERENCE.md`](REFERENCE.md#links-to-items).

### Registering the Studio with ACTIVATE

An organization administrator registers the application once; this changes platform settings and is not done by the Studio.

1. Pick the Studio's address. The application is registered for one exact redirect URI, so the address has to be fixed: reserve a subdomain (`pw subdomains reserve <label>`) and serve the Studio's session on it, giving `https://<label>.<sessions domain>`. Links to items (Copy link) start with this address, so they last as long as it does; a Studio without its own sign-in keeps its links over a relaunch under the same session name, whose subdomain is the same.
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
3. In the deployment form, give **Public Studio URL** (`https://<label>.<sessions domain>`), **Platform Host** under Models, and **ACTIVATE Sign-in Client ID** and **Client Secret** under **Sign-in and Sharing**; the form sets the rest. Started another way, set:

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
- **A site's AI portal:** add it per person under Settings, Accounts, OpenAI-compatible endpoint, with its base URL (usually ending in `/v1`) and key. **Advanced** there takes a portal that wants the key in another header, extra headers, a query string, or typed model names when it does not serve `/models`; **Test** says whether the chosen model calls tools.
- **On ACTIVATE:** with a signed-in pw CLI on the host, nothing needs setting; the Studio uses the CLI's credential for the platform's AI gateway, and every model the account can reach appears in the model list. Otherwise set `PW_API_KEY`. The CLI's current context is used (`PW_CONTEXT` names another); on a single-user install, Settings, Accounts lists the contexts, picks one, and signs the CLI in to another platform with its device code. `PW_ALLOCATION` turns on organization-provided models.
- **Image descriptions:** `ADE_VISION_MODEL` names a vision model, which makes images findable by what they show.
