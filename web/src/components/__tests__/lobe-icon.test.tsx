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
import { act, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'

import { DeferredLobeIcon } from '../lobe-icon'

function ReadyIcon() {
  return <svg aria-label='Ready provider icon' />
}

it('keeps the page usable after an icon download fails and retries when reopening it', async () => {
  const download = vi.fn<() => Promise<{ default: unknown }>>()
  download.mockRejectedValueOnce(new Error('Network unavailable'))
  download.mockResolvedValueOnce({ default: ReadyIcon })
  const props = {
    name: 'Retry',
    iconKey: 'Retry.default',
    loader: download,
    iconProps: { size: 20 },
    size: 20,
  }
  const view = render(
    <>
      <span>Model details</span>
      <DeferredLobeIcon {...props} />
    </>
  )
  await act(async () => {
    await Promise.resolve()
  })
  expect(screen.getByText('R')).toBeVisible()
  expect(screen.getByText('Model details')).toBeVisible()
  view.unmount()
  render(<DeferredLobeIcon {...props} />)
  expect(
    await screen.findByLabelText('Ready provider icon')
  ).toBeInTheDocument()
})
