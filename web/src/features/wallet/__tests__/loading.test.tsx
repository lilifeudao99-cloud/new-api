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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

import { Wallet } from '..'
import { getPaymentIcon } from '../lib/ui'

let queryClient: QueryClient
let requestedPaths: string[]

beforeEach(() => {
  localStorage.clear()
  requestedPaths = []
  useAuthStore
    .getState()
    .auth.setUser({ id: 7, username: 'wallet-user', role: 1 })
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  queryClient.setQueryData(STATUS_QUERY_KEY, { price: 1 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const path = String(url).split('?')[0]
    requestedPaths.push(path)
    let data: unknown
    switch (path) {
      case '/api/user/self':
        data = {
          quota: 5000000,
          used_quota: 500000,
          request_count: 12,
          aff_quota: 0,
        }
        break
      case '/api/user/topup/info':
        data = {
          enable_online_topup: true,
          min_topup: 1,
          amount_options: [10, 20],
          pay_methods: [{ name: 'Alipay', type: 'alipay', icon: 'SiAlipay' }],
        }
        break
      case '/api/user/aff':
        data = 'wallet-referral'
        break
      case '/api/subscription/plans':
        data = []
        break
      case '/api/subscription/self':
        data = { subscriptions: [], all_subscriptions: [] }
        break
      case '/api/user/topup/self':
        data = { items: [], total: 0 }
        break
      default:
        throw new Error(`Unexpected wallet GET: ${path}`)
    }
    return { data: { success: true, data } }
  })
  vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url !== '/api/user/amount') {
      throw new Error(`Unexpected wallet POST: ${url}`)
    }
    return { data: { success: true, data: '20' } }
  })
})

afterEach(() => {
  cleanup()
  queryClient.clear()
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('wallet on-demand loading', () => {
  it('shows the balance and payment methods without requesting unopened order history', async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <Wallet />
      </QueryClientProvider>
    )
    expect(await screen.findByRole('button', { name: 'Alipay' })).toBeVisible()
    expect(screen.getByText('Current Balance')).toBeVisible()
    expect(screen.getByText('12')).toBeVisible()
    expect(requestedPaths).not.toContain('/api/user/topup/self')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('loads order history on demand and can close and reopen it', async () => {
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={queryClient}>
        <Wallet />
      </QueryClientProvider>
    )
    await user.click(
      await screen.findByRole('button', { name: 'Order History' })
    )
    expect(
      await screen.findByRole('dialog', { name: 'Billing History' })
    ).toBeVisible()
    await waitFor(() =>
      expect(requestedPaths).toContain('/api/user/topup/self')
    )
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await user.click(screen.getByRole('button', { name: 'Order History' }))
    expect(
      await screen.findByRole('dialog', { name: 'Billing History' })
    ).toBeVisible()
  })

  it('still opens history when arriving from the payment return link', async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <Wallet initialShowHistory />
      </QueryClientProvider>
    )
    expect(
      await screen.findByRole('dialog', { name: 'Billing History' })
    ).toBeVisible()
    await waitFor(() =>
      expect(requestedPaths).toContain('/api/user/topup/self')
    )
  })

  it('opens the payment review without creating an order and allows cancellation', async () => {
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={queryClient}>
        <Wallet />
      </QueryClientProvider>
    )
    await user.click(await screen.findByRole('button', { name: 'Alipay' }))
    expect(
      await screen.findByRole('alertdialog', { name: 'Confirm Payment' })
    ).toBeVisible()
    expect(api.post).not.toHaveBeenCalledWith(
      '/api/user/pay',
      expect.anything(),
      expect.anything()
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(screen.getByRole('button', { name: 'Alipay' })).toBeVisible()
  })

  it.each(['alipay', 'wxpay', 'stripe'])(
    'preserves the %s payment logo and configured icon name',
    (type) => {
      const view = render(getPaymentIcon(type, 'size-6'))
      const path = view.container.querySelector('path')?.getAttribute('d')
      expect(path).toBeTruthy()
      const names: Record<string, string> = {
        alipay: 'SiAlipay',
        wxpay: 'SiWechat',
        stripe: 'SiStripe',
      }
      view.rerender(getPaymentIcon(type, 'size-6', names[type], 'Payment logo'))
      expect(view.container.querySelector('path')).toHaveAttribute('d', path)
      expect(view.container.querySelector('svg')).toHaveClass('size-6')
    }
  )
})
