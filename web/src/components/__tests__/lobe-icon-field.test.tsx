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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'

import { LobeIconField } from '../lobe-icon-field'

function IconField() {
  const [value, setValue] = useState('OpenAI')
  return (
    <>
      <label htmlFor='icon'>Icon</label>
      <LobeIconField id='icon' value={value} onChange={setValue} />
    </>
  )
}

describe('provider icon selection', () => {
  it('searches names without rendering every logo and previews the selected icon', async () => {
    const user = userEvent.setup()
    render(<IconField />)
    expect((await screen.findByTitle('OpenAI')).closest('svg')).toBeVisible()
    const input = screen.getByRole('combobox', { name: 'Icon' })
    await user.click(input)
    const list = screen.getByRole('listbox')
    expect(
      within(list).getByRole('option', { name: 'Gemini.Color' })
    ).toBeVisible()
    expect(list.querySelector('svg title')).toBeNull()
    await user.clear(input)
    await user.type(input, 'Gemini.Color')
    await user.keyboard('{ArrowDown}{Enter}')
    expect(input).toHaveValue('Gemini.Color')
    expect((await screen.findByTitle('Gemini')).closest('svg')).toBeVisible()
    expect(screen.queryByTitle('OpenAI')).not.toBeInTheDocument()
  })
})
