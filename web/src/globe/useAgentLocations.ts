import { useEffect, useState } from 'react'
import { errorText, request } from '../api'
import type { Agent } from '../model'

export type GeoLocation = {
  ip: string
  latitude: number
  longitude: number
  city: string
  country: string
  country_code: string
}
export type AgentLocation = { public_ips: string[]; location?: GeoLocation; reason?: string }
export type Locations = {
  agents: Record<string, AgentLocation>
  pending: boolean
  error?: string
  database?: string
  database_date?: string
}

// Only geography consumers subscribe. Telemetry changes do not cause lookups;
// endpoint changes refresh immediately, and a slow timer handles lease expiry
// and database availability/updates without adding an Agent wire protocol.
export function useAgentLocations(agents: Agent[], active: boolean) {
  const [data, setData] = useState<Locations>()
  const [error, setError] = useState('')
  const [epoch, setEpoch] = useState(0)
  const key = JSON.stringify(agents.map((a) => [a.id, a.endpoints]))
  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    let running = false
    async function load() {
      if (running || controller.signal.aborted) return
      if (document.hidden) {
        timer = setTimeout(load, 60_000)
        return
      }
      running = true
      clearTimeout(timer)
      let delay = 60_000
      try {
        const incoming = await request<Locations>('/agents/locations', {
          signal: AbortSignal.any([controller.signal, AbortSignal.timeout(15_000)]),
        })
        if (controller.signal.aborted) return
        setData((old) => (JSON.stringify(old) === JSON.stringify(incoming) ? old : incoming))
        setError('')
        if (incoming.pending) delay = 3000
      } catch (e) {
        if (!controller.signal.aborted) {
          setError(errorText(e))
          setData(undefined)
        }
      } finally {
        running = false
        if (!controller.signal.aborted) timer = setTimeout(load, delay)
      }
    }
    const visible = () => {
      if (!document.hidden) void load()
    }
    void load()
    document.addEventListener('visibilitychange', visible)
    return () => {
      controller.abort()
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', visible)
    }
  }, [active, key, epoch])
  return { data, error, retry: () => setEpoch((value) => value + 1) }
}
