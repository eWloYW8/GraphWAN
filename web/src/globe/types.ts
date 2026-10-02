export type GlobeNode = {
  id: string
  name: string
  latitude: number
  longitude: number
  ip: string
  city: string
}
export type GlobeEdge = { id: string; a: string; b: string }
export type Selection = { type: 'node' | 'edge'; id: string } | null
export type Appearance = {
  nodes: Record<string, { color: string; title: string }>
  edges: Record<string, { color: string; dashed: boolean; label?: string }>
  focusedNodes: string[]
  focusedEdges: string[]
  dimmed: boolean
}
export type GlobeGroup = { id: string; name: string; members: string[]; summary: string }
export type GlobeGroupLink = {
  id: string
  node: string
  group: string
  summary: string
  state: string
}
