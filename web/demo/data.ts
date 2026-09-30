export type Location = {
  ip: string
  latitude: number
  longitude: number
  city: string
  country: string
  countryCode: string
  source: string
}
export type GlobeNode = Location & { id: string; name: string }
export type GlobeEdge = { id: string; a: string; b: string }
export type Selection = { type: 'node' | 'edge'; id: string } | null

// Public example IPs, not live GraphWAN agents. Two nodes deliberately share
// an endpoint to demonstrate agents behind the same NAT / in the same city.
export const examples = [
  { id: 'de-1', name: 'europe-01', ip: '5.9.0.1' },
  { id: 'de-2', name: 'europe-02', ip: '5.9.0.1' },
  { id: 'uk', name: 'london', ip: '178.79.128.1' },
  { id: 'us-w', name: 'us-west', ip: '45.32.128.1' },
  { id: 'us-e', name: 'us-east', ip: '45.63.0.1' },
  { id: 'sg', name: 'singapore', ip: '139.162.0.1' },
  { id: 'jp', name: 'tokyo', ip: '139.162.64.1' },
  { id: 'au', name: 'sydney', ip: '45.32.240.1' },
  { id: 'br', name: 'south-america', ip: '200.160.2.3' },
]
export const exampleEdges: GlobeEdge[] = [
  ['de-1', 'de-2'],
  ['de-1', 'uk'],
  ['de-1', 'us-e'],
  ['de-1', 'sg'],
  ['uk', 'us-e'],
  ['us-e', 'us-w'],
  ['us-w', 'jp'],
  ['sg', 'jp'],
  ['sg', 'au'],
  ['jp', 'au'],
  ['us-e', 'br'],
  ['br', 'de-2'],
].map(([a, b]) => ({ id: `${a}:${b}`, a, b }))

export async function locate(ip: string, signal?: AbortSignal): Promise<Location> {
  const response = await fetch(`/__demo/geoip?ip=${encodeURIComponent(ip)}`, { signal })
  const result = await response.json()
  if (!response.ok) throw new Error(result.error || 'GeoIP lookup failed.')
  return result
}

export function distance(a: Location, b: Location): number {
  const rad = Math.PI / 180
  const h =
    Math.sin(((b.latitude - a.latitude) * rad) / 2) ** 2 +
    Math.cos(a.latitude * rad) *
      Math.cos(b.latitude * rad) *
      Math.sin(((b.longitude - a.longitude) * rad) / 2) ** 2
  return Math.round(6371 * 2 * Math.asin(Math.sqrt(Math.min(1, h))))
}
