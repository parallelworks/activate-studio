// Personas: a corpus-shared library, selectable per turn, with the
// deployment able to override a shared name.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = idx

fs.mkdirSync(path.join(kb, '.agents'), { recursive: true })
fs.mkdirSync(path.join(idx, 'extensions', 'agents'), { recursive: true })
fs.writeFileSync(path.join(kb, '.agents', 'reviewer.md'), '---\nname: reviewer\ndescription: reviews proposals\n---\n\nBe exacting.')
fs.writeFileSync(path.join(kb, '.agents', 'shared-one.md'), '---\nname: shared_one\ndescription: from the corpus\n---\n\nCorpus version.')
fs.writeFileSync(path.join(idx, 'extensions', 'agents', 'shared-one.md'), '---\nname: shared_one\ndescription: local override\n---\n\nDeployment version.')
fs.writeFileSync(path.join(idx, 'extensions', 'agents', 'default.md'), 'Standing instructions.')

const { personas, agentPrompt } = await import('../dist/extensions.js')

test('the library merges corpus and deployment personas', () => {
  const names = personas().map(p => p.name).sort()
  assert.deepEqual(names, ['default', 'reviewer', 'shared_one'])
  assert.equal(personas().find(p => p.name === 'reviewer').shared, true)
})

test('a named persona is used, with frontmatter stripped', () => {
  const p = agentPrompt('reviewer')
  assert.equal(p, 'Be exacting.')
  assert.ok(!p.includes('---'), 'frontmatter never reaches the prompt')
})

test('the deployment overrides a shared persona of the same name', () => {
  assert.equal(agentPrompt('shared_one'), 'Deployment version.')
})

test('no name, or an unknown one, falls back to the standing default', () => {
  assert.equal(agentPrompt(), 'Standing instructions.')
  assert.equal(agentPrompt('nope'), 'Standing instructions.')
})

test('a crafted name cannot escape the persona directories', () => {
  fs.writeFileSync(path.join(os.tmpdir(), 'outside.md'), 'SHOULD NOT LOAD')
  assert.equal(agentPrompt('../../../../tmp/outside'), 'Standing instructions.')
})

test('a large skill library does not blow up the tool description', async () => {
  const skillDir = path.join(idx, 'extensions', 'skills')
  fs.mkdirSync(skillDir, { recursive: true })
  const long = 'Produces a structured competitive analysis from corpus material, covering positioning, pricing, and differentiators'
  for (let i = 0; i < 60; i++) {
    fs.writeFileSync(path.join(skillDir, `skill-${i}.md`), `---\nname: skill_${i}\ndescription: ${long}\n---\n\nBody.`)
  }
  const { skillToolSpec } = await import('../dist/chat/tools.js')
  const spec = skillToolSpec()
  const len = spec.function.description.length
  // Unbounded this is ~8,800 characters on every request.
  assert.ok(len < 5000, `description is ${len} chars; expected the clip to bound it`)
  assert.match(spec.function.description, /shortened/, 'says the descriptions are clipped')
  assert.equal(spec.function.parameters.properties.name.enum.length, 60, 'every skill is still selectable')
})
