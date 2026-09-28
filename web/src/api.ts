export class APIError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}
export async function request<T>(
  path: string,
  options: {
    method?: string
    body?: unknown
    csrf?: string
    revision?: number
    signal?: AbortSignal
  } = {},
): Promise<T> {
  const headers: Record<string, string> = {}
  if (options.body !== undefined) headers['Content-Type'] = 'application/json'
  if (options.csrf) headers['X-CSRF-Token'] = options.csrf
  if (options.revision !== undefined) headers['If-Match'] = `"${options.revision}"`
  const response = await fetch(`/api/v1${path}`, {
    method: options.method ?? 'GET',
    credentials: 'same-origin',
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal ?? AbortSignal.timeout(15000),
  })
  if (response.status === 204) return undefined as T
  const data = await response.json().catch(() => {
    throw new APIError(response.status, 'The server returned an unreadable response.')
  })
  if (!response.ok)
    throw new APIError(response.status, data.error ?? `Request failed (${response.status})`)
  return data as T
}
export const errorText = (error: unknown) =>
  error instanceof Error ? error.message : 'Something went wrong. Please retry.'
