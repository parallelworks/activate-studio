import type { StoredConversation, StoredMessage } from '../conversations.js'

/**
 * What a person might type next, read off the turn that just ended. The
 * best suggestions are cheap and specific: after a launch, follow the
 * run; after a search, open the file that answered; after a DAG, run it.
 * They come from the tool calls the server stores with each assistant
 * turn, so no extra model call is made and the suggestion is grounded in
 * what actually happened. A turn that ends by asking the user something
 * gets no suggestions: the next thing to type is the answer.
 */
export interface Suggestion { text: string; why: string }

const RUN_ID = /\b([a-z0-9][a-z0-9_.-]*-\d{5})\b/i

function lastAssistant(c: StoredConversation): StoredMessage | undefined {
  for (let i = c.messages.length - 1; i >= 0; i--) if (c.messages[i].role === 'assistant') return c.messages[i]
  return undefined
}

export function suggestNext(c: StoredConversation): { after: string | null; suggestions: Suggestion[] } {
  const m = lastAssistant(c)
  if (!m) return { after: null, suggestions: [] }
  if (c.messages[c.messages.length - 1]?.role !== 'assistant') return { after: m.id, suggestions: [] }
  const content = String(m.content ?? '')
  const out: Suggestion[] = []
  const push = (text: string, why: string) => { if (!out.some(s => s.text === text) && out.length < 3) out.push({ text, why }) }

  // The reply asked for something; the answer is the next message.
  const tail = content.trim().split('\n').filter(Boolean).pop() ?? ''
  if (/NEEDS DECISION:/i.test(content) || /\?\s*$/.test(tail)) return { after: m.id, suggestions: [] }

  const calls = (m.parts ?? []).filter(p => p.kind === 'tool_call')
  const argsOf = (p: { args: string }): Record<string, unknown> => { try { return JSON.parse(p.args || '{}') } catch { return {} } }
  const failed = calls.filter(p => p.status === 'error')

  for (const p of calls) {
    const a = argsOf(p)
    if (p.name === 'run_workflow' && a.dry_run === false) {
      const slug = RUN_ID.exec(String(p.result ?? ''))?.[1] ?? RUN_ID.exec(content)?.[1]
      if (slug) { push(`Follow run ${slug} until it finishes and tell me the outcome`, 'a run was launched'); push(`Show me the output of run ${slug}`, 'a run was launched') }
    }
    if (p.name === 'run_workflow' && a.dry_run !== false) push(`Launch ${String(a.name ?? 'that workflow')} for real now`, 'the last run was a dry run')
    if (p.name === 'watch_run' || p.name === 'workflow_run_detail') {
      const slug = String(a.run ?? '') || RUN_ID.exec(String(p.result ?? ''))?.[1]
      if (/completed/i.test(String(p.result ?? '')) && slug) push(`Summarize the results of ${slug} and save them to the knowledge base`, 'the run completed')
      if (/\b(error|failed)\b/i.test(String(p.result ?? '')) && slug) push(`Explain why ${slug} failed and fix the submission`, 'the run failed')
    }
    if (p.name === 'get_workflow' || p.name === 'validate_workflow') {
      const name = String(a.name ?? '')
      if (name) push(`Run ${name} on a connected system as a small debug job`, 'a workflow was inspected')
    }
    if (p.name === 'hpc_environments' || p.name === 'list_clusters') {
      const sys = String(a.cluster ?? a.resource ?? '')
      push(sys ? `Run activatebatch on ${sys} as a debug job that prints the hostname` : 'Run a small debug job on the system with the most free nodes', 'systems were listed')
    }
    if (p.name === 'delegate') push('Show me how the delegated agents are doing', 'work was delegated')
    if (p.name === 'search_kb') {
      const cite = /\[([^\]]+\.(?:md|pdf|docx|pptx|xlsx|txt))\]\(#open=file:[^)]+\)/i.exec(content)?.[1]
      if (cite) push(`Open ${cite} and summarize it`, 'a search cited it')
    }
    if (p.name === 'write_kb_file') {
      const path = String(a.path ?? '')
      if (path) push(`Open ${path} in the library`, 'a file was written')
    }
  }
  if (failed.length && out.length < 3) push(`Retry the ${failed[0].name} step that failed and explain what went wrong`, 'a tool call failed')

  if (!calls.length && content.length > 400) {
    push('Give me the sources behind that, as clickable paths', 'a long answer without tool calls')
    push('Save that as a short note in the knowledge base', 'a long answer')
  }
  return { after: m.id, suggestions: out }
}
