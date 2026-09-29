import { beforeEach, expect, it, vi } from 'vitest'
import { APIError, request } from './api'
import type { State } from './model'
import { saveConfiguration } from './saveConfiguration'

vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  request: vi.fn(),
}))
const call = vi.mocked(request)
const options = { method: 'PUT', body: { name: 'draft' }, csrf: 'csrf', revision: 10 }
const latest: State = { schema: 1, revision: 11, networks: [], agents: [] }
const conflict = () => new APIError(409, 'configuration changed; reload and retry')
beforeEach(() => vi.resetAllMocks())

it('retries an unrelated revision change with the original draft and fresh revision', async () => {
  const saved = { ...latest, revision: 12 }
  call.mockRejectedValueOnce(conflict()).mockResolvedValueOnce(latest).mockResolvedValueOnce(saved)
  const unchanged = vi.fn().mockReturnValue(true)
  const updated = vi.fn()
  expect(await saveConfiguration('/networks/n', options, unchanged, updated)).toBe(saved)
  expect(unchanged).toHaveBeenCalledWith(latest)
  expect(updated).toHaveBeenCalledWith(latest)
  expect(call.mock.calls).toEqual([
    ['/networks/n', options],
    ['/state'],
    ['/networks/n', { ...options, revision: 11 }],
  ])
})

it('preserves a draft when its target was changed or deleted', async () => {
  call.mockRejectedValueOnce(conflict()).mockResolvedValueOnce(latest)
  const updated = vi.fn()
  await expect(saveConfiguration('/networks/n', options, () => false, updated)).rejects.toThrow(
    'Your draft is preserved',
  )
  expect(call).toHaveBeenCalledTimes(2)
  expect(updated).toHaveBeenCalledWith(latest)
})

it('bounds retries during continuous updates', async () => {
  call.mockImplementation(async (path) => {
    if (path === '/state') return latest
    throw conflict()
  })
  await expect(saveConfiguration('/networks/n', options, () => true, vi.fn())).rejects.toThrow(
    'please save again',
  )
  expect(call.mock.calls.filter(([path]) => path !== '/state')).toHaveLength(5)
})

it.each([400, 401, 403, 500])('does not retry HTTP %i', async (status) => {
  const error = new APIError(status, 'failed')
  call.mockRejectedValueOnce(error)
  await expect(saveConfiguration('/networks/n', options, () => true, vi.fn())).rejects.toBe(error)
  expect(call).toHaveBeenCalledTimes(1)
})
