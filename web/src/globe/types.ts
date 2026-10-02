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
  edges: Record<string, { color: string; dashed: boolean }>
  focusedNodes: string[]
  focusedEdges: string[]
  dimmed: boolean
}
