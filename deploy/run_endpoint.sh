#!/usr/bin/env bash
# Serve ade-studio as a session in the ACTIVATE account.
# Requires: pnpm build done, PW_API_KEY exported, pw CLI authenticated.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

# Deployment-specific values (KB_LABEL, SUGGESTED_PROMPTS, APP_USER_*, keys)
# live in a gitignored .env, never in the repo.
if [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi

# One instance only: stop any previous endpoint wrapper and server.
pkill -f 'endpoints run --name activate-studio' 2>/dev/null || true
pkill -f 'node server/dist/main.js' 2>/dev/null || true
sleep 1

exec pw endpoints run --name activate-studio --subdomain activate-studio -- node server/dist/main.js
