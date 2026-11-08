/**
 * The server's second import (main.ts): the state's format is checked
 * before any module reads settings or conversations (stateFormat.ts).
 */
import { reconcileState } from './stateFormat.js'
import { buildInfo } from './version.js'

try {
  reconcileState(buildInfo().version, msg => console.log(msg))
} catch (e) {
  console.log(`state: the state format could not be checked: ${(e as Error)?.message ?? e}`)
}
