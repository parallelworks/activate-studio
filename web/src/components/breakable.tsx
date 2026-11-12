import { Children, Fragment, cloneElement, isValidElement, type ReactNode } from 'react'

/**
 * Text that may wrap after a path's separators (/ _ . -) and nowhere else
 * inside a word. A file path in a narrow table column then breaks between
 * its parts ("reports/" "site-" "survey-" "notes.md") where it used to break
 * anywhere, mid word; <wbr> adds the chance to break without adding a
 * character, so copying the text gives it back unchanged. Strings nested in
 * elements (a link's label inside a span) are handled too.
 */
const SEP = /(?<=[/_.\\-])(?=[^/_.\\-])/

function breakString(s: string, key: string): ReactNode {
  if (s.length < 12 || !SEP.test(s)) return s
  const parts = s.split(SEP)
  return parts.map((p, i) => <Fragment key={`${key}-${i}`}>{p}{i < parts.length - 1 && <wbr />}</Fragment>)
}

export function breakable(children: ReactNode, key = 'b'): ReactNode {
  return Children.map(children, (c, i) => {
    if (typeof c === 'string') return breakString(c, `${key}${i}`)
    if (isValidElement<{ children?: ReactNode }>(c) && c.props.children !== undefined) {
      return cloneElement(c, undefined, breakable(c.props.children, `${key}${i}-`))
    }
    return c
  })
}
