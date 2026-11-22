/** Commands longer than this show their start first. */
const HEAD_LINES = 8
const HEAD_CHARS = 600

/** How a command shows before it is opened: whole when short, else its
 *  first lines. Approve runs the whole command, so it is offered only once
 *  the whole command is on screen. */
export function commandHead(text: string): { head: string; long: boolean; lines: number } {
  const lines = text.split('\n')
  const long = text.length > HEAD_CHARS || lines.length > HEAD_LINES
  return { head: long ? lines.slice(0, HEAD_LINES).join('\n').slice(0, HEAD_CHARS) : text, long, lines: lines.length }
}
