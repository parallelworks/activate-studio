// Every piece of the before inventory must be somewhere in the after one:
// a line of text, a control's name, a menu item, a hover title. Prints what
// is not found, scenario by scenario, for review.
import fs from 'node:fs'
const [DIR, TAB = 'libraries'] = process.argv.slice(2)
const norm = x => String(x ?? '').replace(/\s+/g, ' ').trim()
let missingTotal = 0
for (const f of fs.readdirSync(DIR).filter(f => f.startsWith(`before-${TAB}-`) && f.endsWith('.json'))) {
  const name = f.slice(`before-${TAB}-`.length, -5)
  const after = `${DIR}/after-${TAB}-${name}.json`
  if (!fs.existsSync(after)) { console.log(`## ${name}: no after inventory`); continue }
  const b = JSON.parse(fs.readFileSync(`${DIR}/${f}`)), a = JSON.parse(fs.readFileSync(after))
  const hay = norm([...a.text, ...a.controls.map(c => c.name), ...a.titles, ...a.menus.flatMap(m => Array.isArray(m.items) ? m.items : [])].join(' \n '))
  const miss = []
  for (const t of b.text) if (!hay.includes(norm(t))) miss.push(`text: ${t}`)
  // Controls as a multiset of (name, enabled): each one before needs its own one after.
  // A Copy button is now named by what it copies ("Copy Index" became "Copy the index path of <library>").
  const key = (n, d) => `${n}|${d ? 'disabled' : 'enabled'}`
  const pool = new Map()
  const add = k => pool.set(k, (pool.get(k) ?? 0) + 1)
  for (const c of a.controls) add(key(norm(c.name).replace(/^Copy the (.+) path of .*$/, (_m, w) => `Copy ${w[0].toUpperCase()}${w.slice(1)}`), c.disabled))
  for (const m of a.menus) for (const i of Array.isArray(m.items) ? m.items : []) {
    const dis = / \(disabled\)$/.test(i)
    const label = norm(i).replace(/ \(disabled\)$/, '')
    const nm = label.startsWith('Remove') ? `Remove ${m.trigger.replace(/^More actions for /, '')}` : label.startsWith('Delete index') ? 'Delete index' : label
    add(key(nm, dis))
  }
  for (const c of b.controls) {
    const k = key(norm(c.name), c.disabled)
    if (pool.get(k)) pool.set(k, pool.get(k) - 1)
    else miss.push(`control: ${c.role} "${c.name}" ${c.disabled ? 'disabled' : 'enabled'}`)
  }
  for (const t of b.titles) if (!hay.includes(norm(t))) miss.push(`title: ${t}`)
  missingTotal += miss.length
  console.log(`## ${name}: ${b.text.length} lines, ${b.controls.length} controls before; ${a.text.length} lines, ${a.controls.length} controls, ${a.menus.flatMap(m => Array.isArray(m.items) ? m.items : []).length} menu items after; ${miss.length} not found`)
  for (const m of miss) console.log(`  - ${m}`)
}
console.log(`total not found: ${missingTotal}`)
