// Where a keyboard user's focus goes when the session waits on them. The
// composer turns disabled while a question, plan or approval waits, which
// drops focus to the page; without this, the card was reached by Tab only
// after every earlier message's buttons and tool rows.

const FIELDS = 'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), [tabindex="0"]'

/** Focus is the person's to keep when it sits anywhere they put it, outside
 *  the session or in a field of it; it is free when it fell to the page, or,
 *  as the chat opens, when it is on the conversations list's row that opened
 *  it: what the chat waits on is the next step. */
function focusIsFree(opening = false): boolean {
  const a = document.activeElement
  if (!a || a === document.body || a === document.documentElement) return true
  return opening && !!a.closest('.chat-canvas aside li')
}

/** Moves focus to the first answer of what the session waits on: the plan
 *  card under the transcript, else the last open approval card. */
export function focusWaiting(pane: HTMLElement | null, opening = false): boolean {
  if (!pane || !focusIsFree(opening)) return false
  const plan = pane.querySelector<HTMLElement>('[data-testid="pwcode-plan-answer"]')
  const cards = [...pane.querySelectorAll<HTMLElement>('[data-testid="approval-part"]')]
  for (const card of plan ? [plan] : cards.reverse()) {
    const first = card.querySelector<HTMLElement>(FIELDS)
    if (first) { first.focus(); return true }
  }
  return false
}

/** Back to the session's message box once nothing waits, when the answered
 *  card took focus with it. */
export function focusComposer(pane: HTMLElement | null): boolean {
  if (!pane || !focusIsFree()) return false
  const boxes = [...pane.querySelectorAll<HTMLTextAreaElement>('textarea:not([disabled])')]
  const box = boxes[boxes.length - 1]
  if (!box) return false
  box.focus()
  return true
}
