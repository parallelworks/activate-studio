package tasks

// runnerYAML is tasks.ts's RUNNER_YAML: one job on the target system's
// login node that runs pw code and brackets its output in markers.
const runnerYAML = `permissions:
  - '*'
'on':
  execute:
    inputs:
      resource:
        label: Agent host
        type: compute-clusters
        autoselect: true
        optional: false
      prompt:
        label: Agent prompt
        type: editor
      model:
        label: Model
        type: string
      agent_name:
        label: Agent name
        type: string
      board_url:
        label: Studio board URL
        type: string
        optional: true
      board_token:
        label: Board token
        type: string
        optional: true
      timeout_seconds:
        label: Timeout seconds
        type: number
        default: 7200
jobs:
  agent:
    ssh:
      remoteHost: ${{ inputs.resource.ip }}
    steps:
      - name: Run agent
        run: |
          set -e
          WORK="$PWD/agent-work"
          mkdir -p "$WORK/.agents"
          cat > "$WORK/prompt.txt" << 'STUDIO_PROMPT_EOF'
          ${{ inputs.prompt }}
          STUDIO_PROMPT_EOF
          if [ -n "${{ inputs.board_url }}" ]; then
            cat > "$WORK/.agents/settings.local.json" << 'STUDIO_MCP_EOF'
          {"mcpServers":{"task":{"type":"http","url":"${{ inputs.board_url }}/api/mcp","headers":{"Authorization":"Bearer ${{ inputs.board_token }}","X-Task-Agent":"${{ inputs.agent_name }}"}}}}
          STUDIO_MCP_EOF
          fi
          set +e
          timeout "${{ inputs.timeout_seconds }}" pw code -p "$(cat "$WORK/prompt.txt")" -o json --permission-mode read-only -w "$WORK" -m "${{ inputs.model }}" > "$WORK/out.json" 2> "$WORK/err.log"
          RC=$?
          set -e
          echo "STUDIO_RESULT_BEGIN"
          cat "$WORK/out.json" 2>/dev/null || true
          echo ""
          echo "STUDIO_RESULT_END"
          tail -c 4000 "$WORK/err.log" 2>/dev/null || true
          exit $RC
`
