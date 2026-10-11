# Delegated agents

When delegation is on (Settings, External access, Delegation), the assistant can split a request into subtasks and give each to an agent. The task tree, the board, and results under `tasks/<task id>/` in the knowledge base are the same however the agents run. The same section's "Agent execution" (environment variable `AGENT_EXECUTION`) chooses how they run:

| Setting | Agents run as |
|---|---|
| `runner` (default) | one-shot `pw code -p` runs beside the server, or, for a campaign, platform workflow runs on a named system |
| `sessions` | sessions in the pw code daemon on the Studio's host |
| `both` | either; the assistant chooses per task, defaulting to sessions |

A campaign on another system always uses the runner. Sessions on remote systems need the platform's agents routes and are a later step.

The task view names the machine its agents run on: the task's system for a campaign, the daemon's host for session agents, and the Studio's host for runners. It appears once in the task's header, and on each agent's row only when a task's agents are on different machines.

## Session agents

The Studio talks to the pw code daemon over its Unix socket (`$XDG_STATE_HOME/pw/code-<hostname>.sock`, or `PW_CODE_SOCKET`), which only the account that runs the Studio can open. When no daemon is running, the first session agent starts one with the Studio's own platform context (`PW_CONTEXT`), since a daemon started without a context binds to whichever context is current for that account. The daemon must speak protocol 2.

Each agent is a session created read-only, with the task's board registered inline in the request and its tools pre-approved: the board tools and the knowledge base tools the Studio exposes over MCP, reached on the loopback address with the task's token. Nothing else is pre-approved. The token travels only in the request body, never in a process's arguments or a file. Sessions are created with remote control on, so they can also be watched or driven from ACTIVATE's agents page.

The Studio follows each session about every two seconds (`STUDIO_SESSION_POLL_MS`):

- its history becomes the agent's live output;
- an approval request makes the agent `input-required`, posts the request to the board, and shows it under "Waiting for your approval", where Approve or Deny answers the session;
- a direction typed under a working agent's output reaches the session mid-turn (`POST /api/tasks/<id>/agents/<name>/direction`);
- the end of the turn completes the agent from its last reply, through the same result contract as the runner (a JSON object with `result` and optional `subtasks`);
- a turn that ends with no reply, such as one refused for an expired platform login, fails the agent with that reason.

Stopping a task interrupts its sessions. Sessions belong to the daemon, so a Studio restart reattaches to agents that were still working. A session can be opened in a terminal on the host with `pw code -r <session id>`.

`GET /api/tasks/runtime` reports the setting and what the local daemon reports: its version, whether it is usable, and its remote session policy.

## The runner

A one-shot run has no board: pw code loads a workspace settings file only after someone approves it interactively, and the alternatives would put the task token in the command line or the run's inputs. Its output arrives when it finishes. Campaign runs are not sent the board token, since the Studio's public address sits behind the platform's session proxy, which refuses it.

## pw code agents at sites

The assistant can run pw code agents directly, on one of the user's clusters or, where offered, on the Studio's host. These are pw code sessions the user could also start in a terminal; the assistant's tools start, follow, and steer them (`agent_sites`, `agent_sessions`, `start_agent`, `agent_status`, `agent_steer`, `agent_goal`, `agent_approve`, `agent_stop`).

*Transport.* A cluster's daemon is reached the way `pw code attach` reaches it: the Studio server runs `pw ssh <cluster> pw code daemon proxy --ensure`, which bridges the ssh session to that machine's daemon socket and starts the daemon if none runs, and speaks the daemon's HTTP API over the pipe (`server/src/pwcodeBridge.ts`). One keep-alive bridge per user and cluster carries every request; the first pays for the ssh setup (about 5 seconds), later ones take about a quarter of a second. The bridge closes after 10 idle minutes. This needs only `pw ssh` and a pw CLI with `pw code daemon proxy` on the cluster (v7.120 or later), not the platform's agent routes.

*Credentials.* A user's agents act with the key their chat uses (Settings, Model access), so each connection is that user's and the session runs as them on the cluster, with their files and their pw login there for models. The deployment's key stands in only when sign-in is off and the Studio has one user. Through the bridge the Studio has the owner's authority over that daemon, so it sets each session's permission mode: accept-edits by default (edits in the workspace, asks before commands), or read-only or plan.

*Goals.* `start_agent` creates the session in a workspace (a new directory under `~/studio-agents` unless one is named), sends the objective, and, when a goal is given, sets it through the daemon's `/goal` route. The goal is checked after every turn by an evaluator; while it is not met the session starts another turn on its own. pw code puts no limit on a goal, so the Studio writes one into the condition ("if this is not met within N turns, stop and say what remains", 20 by default). `agent_status` reports the goal's state and the last check (turns, met or not and why), approvals waiting, and the end of the transcript. Approvals are answered only on the user's decision in the conversation.

*Local site.* The Studio's host is a site only when sign-in is off, or when `AGENT_LOCAL_SITE=1`: its daemon belongs to the account the Studio runs as, and a viewer acting there would act as that account.

*Routes.* `GET /api/agent-sites`, `GET /api/agent-sites/<site>/status`, `GET` and `POST /api/agent-sites/<site>/sessions`, `GET /api/agent-sites/<site>/sessions/<id>`, and `POST .../goal`, `.../steer`, `.../approvals/<approval>`, and `.../interrupt`.
