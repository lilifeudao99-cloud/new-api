/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  isChunkLoadError,
  reloadForChunkLoadError,
} from './chunk-load-recovery'

function chunkError() {
  const error = new Error(
    'Loading chunk 3239 failed.\n(missing: /static/js/async/3239.8dd4101a83.js)'
  )
  error.name = 'ChunkLoadError'
  return error
}

describe('isChunkLoadError', () => {
  it('recognises bundler and native dynamic import failures', () => {
    expect(isChunkLoadError(chunkError())).toBe(true)
    expect(isChunkLoadError(new Error('Loading CSS chunk 240 failed.'))).toBe(
      true
    )
    expect(
      isChunkLoadError(
        new TypeError('Failed to fetch dynamically imported module: /x.js')
      )
    ).toBe(true)
    expect(
      isChunkLoadError(new TypeError('Importing a module script failed.'))
    ).toBe(true)
  })

  it('ignores other errors', () => {
    expect(
      isChunkLoadError(new Error('Request failed with status code 500'))
    ).toBe(false)
    expect(isChunkLoadError('Loading chunk 1 failed')).toBe(false)
    expect(isChunkLoadError(null)).toBe(false)
  })
})

describe('reloadForChunkLoadError', () => {
  const reload = vi.fn()
  const originalLocation = window.location

  beforeEach(() => {
    window.sessionStorage.clear()
    reload.mockReset()
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...originalLocation, reload },
    })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: originalLocation,
    })
    vi.useRealTimers()
  })

  it('reloads once and throttles repeated failures', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-18T00:00:00Z'))
    expect(reloadForChunkLoadError(chunkError())).toBe(true)
    expect(reloadForChunkLoadError(chunkError())).toBe(false)
    expect(reload).toHaveBeenCalledTimes(1)

    vi.setSystemTime(new Date('2026-10-18T00:01:01Z'))
    expect(reloadForChunkLoadError(chunkError())).toBe(true)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  it('does not reload for unrelated errors', () => {
    expect(reloadForChunkLoadError(new Error('boom'))).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })
})
