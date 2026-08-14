/** Conversation label scope: the chat adapter reads this at request time, so
 *  it lives outside React state. Set from the ChatView scope chips. */
let scope: string[] = []

export function getLabelScope(): string[] {
  return scope
}

export function setLabelScope(tags: string[]): void {
  scope = tags
}
