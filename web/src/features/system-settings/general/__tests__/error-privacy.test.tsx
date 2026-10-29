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
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { SystemBehaviorSection } from '../system-behavior-section'

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <SystemBehaviorSection
          defaultValues={{
            DefaultCollapseSidebar: false,
            DemoSiteEnabled: false,
            SelfUseModeEnabled: false,
            general_setting: {
              hide_upstream_error: false,
              upstream_error_message: '',
            },
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

async function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('switch', { name: 'Hide upstream error details' })
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('enabling error privacy enables the message field and saves both option keys', async () => {
  const user = userEvent.setup()
  const toggle = await renderSettings()
  const input = screen.getByRole('textbox', { name: 'Fallback error message' })
  expect(toggle).not.toBeChecked()
  expect(input).toBeDisabled()
  expect(input).toHaveAttribute(
    'placeholder',
    'Leave blank to use the default message for unrecognized errors'
  )
  expect(
    screen.getByText(
      'Common error causes take priority. This message is only used for unrecognized errors. A local request ID is included.'
    )
  ).toBeVisible()
  await user.click(toggle)
  expect(input).toBeEnabled()
  await user.type(input, 'Please contact support')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => {
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'general_setting.hide_upstream_error',
      value: true,
    })
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'general_setting.upstream_error_message',
      value: 'Please contact support',
    })
  })
})

test('an empty message is accepted and disabling privacy preserves the draft', async () => {
  const toggle = await renderSettings()
  const input = screen.getByRole('textbox', { name: 'Fallback error message' })
  fireEvent.click(toggle)
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'general_setting.hide_upstream_error',
      value: true,
    })
  )
  fireEvent.change(input, { target: { value: 'Saved draft' } })
  fireEvent.click(toggle)
  expect(input).toBeDisabled()
  expect(input).toHaveValue('Saved draft')
})
