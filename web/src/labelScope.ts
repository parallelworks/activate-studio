/** Conversation label scope: the chat adapter reads this at request time, so
 *  it lives outside React state. Set from the ChatView scope chips. */
let scope: string[] = []

export function getLabelScope(): string[] {
  return scope
}

export function setLabelScope(tags: string[]): void {
  scope = tags
}


/** The persona chosen for the current conversation, read by the adapter
 *  when it sends a turn. Module state rather than a prop because the
 *  composer belongs to the chat package. */
let persona: string | null = null
export function setPersona(name: string | null): void { persona = name || null }
export function getPersona(): string | null { return persona }
