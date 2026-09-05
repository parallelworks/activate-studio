import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Vitest runs without globals here, so Testing Library's automatic
// unmount does not register itself; each test starts from an empty body.
afterEach(() => cleanup())
