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
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

const queryClients: QueryClient[] = []

function makeLog(other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 5,
    content: 'request rejected',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-test',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: 'channel',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(isAdmin: boolean, other?: LogOtherData): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt: freshAt })
  queryClients.push(queryClient)

  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(
          other ?? {
            admin_info: {
              reject_reason: 'blocked by channel policy',
            },
          }
        )}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const queryClient of queryClients) {
    queryClient.clear()
  }
  queryClients.length = 0
})

describe('usage log reject reason', () => {
  const streamFailure = {
    stream_status: {
      status: 'error',
      end_reason: 'eof',
      response_status: 'failed',
    },
    admin_info: {
      upstream_stream_error: {
        event: 'response.failed',
        status_code: 422,
        code: 'upstream_rejected',
        type: 'invalid_request_error',
        message: 'The upstream rejected this request',
      },
      usage_source: 'missing',
      upstream_billing_status: 'unknown',
    },
    billing_decision: {
      action: 'waived',
      reason: 'failed_without_output_or_usage',
    },
  }

  test('shows detailed upstream stream errors and unverified billing to admins', () => {
    renderDetails(true, streamFailure)
    expect(
      screen.getByText('The upstream rejected this request')
    ).toBeInTheDocument()
    expect(screen.getByText('422')).toBeInTheDocument()
    expect(screen.getByText('No usage reported')).toBeInTheDocument()
    expect(screen.getByText('Unknown (not verified)')).toBeInTheDocument()
    expect(
      screen.getByText(
        'No charge: upstream failure without output or reported usage'
      )
    ).toBeInTheDocument()
  })

  test('keeps upstream stream diagnostics private while explaining the waived charge', () => {
    renderDetails(false, streamFailure)
    expect(screen.queryByText('The upstream rejected this request')).toBeNull()
    expect(screen.queryByText('upstream_rejected')).toBeNull()
    expect(screen.queryByText('Unknown (not verified)')).toBeNull()
    expect(
      screen.getByText(
        'No charge: upstream failure without output or reported usage'
      )
    ).toBeInTheDocument()
  })

  test('shows the nested admin reject reason to admins', () => {
    renderDetails(true)

    expect(screen.getByText('Reject Reason')).toBeInTheDocument()
    expect(screen.getByText('blocked by channel policy')).toBeInTheDocument()
  })

  test('hides the nested admin reject reason from non-admin users', () => {
    renderDetails(false)

    expect(screen.queryByText('Reject Reason')).toBeNull()
    expect(screen.queryByText('blocked by channel policy')).toBeNull()
  })
})
