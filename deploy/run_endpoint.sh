#!/usr/bin/env bash
# Serve ade-studio as a session in the ACTIVATE account.
# Requires: pnpm build done, PW_API_KEY exported, pw CLI authenticated.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

if [ -z "${PW_API_KEY:-}" ]; then
  echo "warning: PW_API_KEY not set; chat will be disabled" >&2
fi

exec pw endpoints run --name ade-studio -- node server/dist/main.js
