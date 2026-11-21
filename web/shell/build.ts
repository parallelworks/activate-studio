import { createHash } from 'node:crypto'

/** What sw.js keeps for a build: the page, the files it names under
 *  /assets/ (the entry script and stylesheet), and the icons in public/.
 *  The build id is the page's hash; the page names every other file's
 *  hash, through the entry script. */
export function shellOf(html: string, icons: string[]): { build: string; entry: string[]; files: string[] } {
  const entry = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+)"/g)].map(m => m[1])
  const build = createHash('sha256').update(html).digest('hex').slice(0, 16)
  return { build, entry, files: ['./', ...entry.map(f => '.' + f), ...icons.map(f => './' + f)] }
}

const PLACEHOLDER = /\/\* SHELL \*\/[^\n]*\/\* END SHELL \*\//

/** web/shell/sw.js with a build's shell in place of its placeholder. */
export function fillShell(src: string, shell: ReturnType<typeof shellOf>): string {
  if (!PLACEHOLDER.test(src)) throw new Error('web/shell/sw.js: no SHELL placeholder')
  return src.replace(PLACEHOLDER, () => JSON.stringify(shell))
}
