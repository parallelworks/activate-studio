import { describe, expect, it } from 'vitest'
import { commandHead } from '../src/commandHead'

describe('a long command', () => {
  it('counts lines as well as characters', () => {
    const nine = Array.from({ length: 9 }, (_, i) => `echo ${i}`).join('\n')
    expect(commandHead(nine)).toEqual({ head: nine.split('\n').slice(0, 8).join('\n'), long: true, lines: 9 })
  })
})
