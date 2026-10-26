/**
 * A first look at a library's ssh options as they are typed, so the form
 * can say what is wrong before a test: the flags the server takes (-J, -p,
 * -i, -l, -F, -o Keyword=value, -4, -6, -C), no quotes, and the keywords it
 * refuses outright. The server checks everything again (parseSshOptions in
 * server/src/remoteExec.ts), including each value and the full list of
 * keywords it allows; this only catches the common mistakes early.
 */
const REFUSED: Record<string, string> = {
  proxycommand: 'ProxyCommand runs a local command: use -J, or a Host entry in ~/.ssh/config',
  localcommand: 'LocalCommand runs a local command',
  permitlocalcommand: 'PermitLocalCommand runs a local command',
  knownhostscommand: 'KnownHostsCommand runs a local command',
  remotecommand: 'RemoteCommand replaces the command the Studio runs',
  batchmode: 'the Studio always runs ssh without prompts',
  controlmaster: 'the Studio shares connections itself',
  controlpath: 'the Studio shares connections itself',
  controlpersist: 'the Studio shares connections itself',
}

/** What is wrong with the options, or null when they look right. */
export function sshOptionsProblem(text: string): string | null {
  const s = text.trim()
  if (!s) return null
  if (s.length > 500) return 'At most 500 characters.'
  if (/['"`\\]/.test(s)) return 'No quotes or backslashes; a value with a space belongs in a Host entry in ~/.ssh/config.'
  const words = s.split(/\s+/)
  for (let i = 0; i < words.length; i++) {
    const w = words[i]
    if (w === '-4' || w === '-6' || w === '-C') continue
    const m = /^-([JpilFo])(.*)$/.exec(w)
    if (!m) return `${w} is not one of -J, -p, -i, -l, -F, -o, -4, -6 or -C.`
    const value = m[2] || words[++i]
    if (!value) return `-${m[1]} needs a value.`
    if (m[1] === 'o') {
      const kv = /^([A-Za-z0-9]+)=/.exec(value)
      if (!kv) return `-o ${value}: write it as Keyword=value.`
      const why = REFUSED[kv[1].toLowerCase()]
      if (why) return `${kv[1]} is not allowed here: ${why}.`
    }
  }
  return null
}
