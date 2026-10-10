# Delegated agents

When delegation is on (Settings, External access, Delegation), the assistant can split a request into subtasks and give each to an agent. The task tree, the board, and results under `tasks/<task id>/` in the knowledge base are the same however the agents run. The same section's "Agent execution" (environment variable `AGENT_EXECUTION`) chooses how they run:

| Setting | Agents run as |
|---|---|
| `runner` (default) | one-shot `pw code -p` runs beside the server, or, for a campaign, platform workflow runs on a named system |
| `sessions` | sessions in the pw code daemon on the Studio's host |
| `both` | either; the assistant chooses per task, defaulting to sessions |

A campaign on another system always uses the runner. Sessions on remote systems need the platform's agents routes and are a later step.

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
