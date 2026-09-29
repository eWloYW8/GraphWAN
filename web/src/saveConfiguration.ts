import { APIError, request } from './api'
import type { State } from './model'

// The controller uses a global revision, including automatic endpoint discovery.
// Retry only after confirming that everything this edit replaces is unchanged.
export async function saveConfiguration(
  path: string,
  options: { method: string; body: unknown; csrf: string; revision: number },
  unchanged: (state: State) => boolean,
  updated: (state: State) => void,
): Promise<State> {
  let revision = options.revision
  for (let attempt = 0; attempt < 5; attempt++) {
    try {
      return await request<State>(path, { ...options, revision })
    } catch (error) {
      if (!(error instanceof APIError) || error.status !== 409) throw error
      const latest = await request<State>('/state')
      updated(latest)
      if (!unchanged(latest))
        throw new APIError(
          409,
          'These settings changed while you were editing. Your draft is preserved.',
        )
      revision = latest.revision
    }
  }
  throw new APIError(
    409,
    'Configuration is updating frequently. Your draft is preserved; please save again.',
  )
}
