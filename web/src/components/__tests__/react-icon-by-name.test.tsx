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
import { render, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { ReactIconByName } from '../react-icon-by-name'

const { simpleIconsPackLoaded } = vi.hoisted(() => ({
  simpleIconsPackLoaded: vi.fn(),
}))

vi.mock('react-icons/si', async (importOriginal) => {
  simpleIconsPackLoaded()
  return await importOriginal<typeof import('react-icons/si')>()
})

describe('ReactIconByName payment icon loading', () => {
  it('renders built-in payment icons without loading the Simple Icons pack', async () => {
    const view = render(<ReactIconByName name='SiAlipay' />)

    expect(view.container.querySelector('path')).toBeTruthy()
    expect(simpleIconsPackLoaded).not.toHaveBeenCalled()
  })

  it('still resolves other Simple Icons on demand', async () => {
    const view = render(<ReactIconByName name='SiGoogle' />)

    await waitFor(() => expect(simpleIconsPackLoaded).toHaveBeenCalledOnce())
    await waitFor(() =>
      expect(view.container.querySelector('path')).toBeTruthy()
    )
  })
})
