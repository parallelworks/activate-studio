import { readFileSync } from 'node:fs'
import { render } from '@testing-library/react'
import type { PwCodeMachine, PwCodeSession } from '@parallelworks/code'
import { describe, expect, it } from 'vitest'
import { SessionMark } from '../src/code/parts'
import { runLook, statusLook } from '../src/statusLook'
import { RunStatusIcon } from '@parallelworks/ui'

const css = readFileSync('src/styles.css', 'utf8')
const session = (status: string) => ({ machineId: 'm1', id: 's1', name: 's', status, workspace: '/x' }) as unknown as PwCodeSession
const machine = (status = 'connected') => ({ id: 'm1', name: 'examplecluster', status }) as unknown as PwCodeMachine
const html = (s: PwCodeSession, m: PwCodeMachine) => render(<SessionMark session={s} machine={m} size="row" />).container.innerHTML

describe('the state marks', () => {
  it('are blue for a session at work, and for one whose machine is starting', () => {
    expect(html(session('running'), machine())).toMatch(/theme-info/)
    expect(html(session('idle'), machine('starting'))).toMatch(/theme-info/)
    expect(html(session('starting'), machine())).toMatch(/data-status="started"/)
    expect(html(session('queued'), machine())).toMatch(/data-status="pending"/)
    expect(html(session('idle'), machine('unreachable'))).not.toMatch(/theme-info/)
  })
  it('are blue for a run waiting to start', () => {
    const { container } = render(<RunStatusIcon status="pending" label="" />)
    expect(container.querySelector("span[data-status='pending'] > svg")).not.toBeNull()
    expect(css).toMatch(/span\[data-status='pending'\] > svg \{ color: var\(--theme-info\)/)
    for (const s of ['pending', 'queued', 'starting', 'running']) expect(statusLook(s).variant).toBe('info')
    expect(runLook('queued')).toBe('pending')
    expect(runLook('starting')).toBe('started')
    expect(runLook('running')).toBe('running')
    expect(runLook('cancelled')).toBe('canceled')
  })
  it('keep the ring’s track clear of a selected row’s fill', () => {
    const { container } = render(<RunStatusIcon status="running" label="" />)
    expect(container.querySelector("[data-status-icon='ring'] > circle:first-child")).not.toBeNull()
    expect(css).toMatch(/\[data-status-icon='ring'\] > circle:first-child \{ opacity: 0\.5; \}/)
  })
})
