import { Position } from '@xyflow/react'

export type Rect = { x: number; y: number; width: number; height: number }
export type Anchor = { x: number; y: number; position: Position }
export type Anchors = { source: Anchor; target: Anchor }
export const center = (rect: Rect) => ({ x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 })

// Intersect the ray toward the other node with this card's perimeter. The
// attachment is continuous along each side, not a predefined handle location.
export function boundary(rect: Rect, toward: { x: number; y: number }, fallback = 1): Anchor {
  const origin = center(rect)
  let dx = toward.x - origin.x
  const dy = toward.y - origin.y
  if (dx === 0 && dy === 0) dx = fallback
  const hw = Math.max(1, rect.width / 2)
  const hh = Math.max(1, rect.height / 2)
  const scale = 1 / Math.max(Math.abs(dx) / hw, Math.abs(dy) / hh)
  const horizontal = Math.abs(dx) / hw >= Math.abs(dy) / hh
  const position = horizontal
    ? dx > 0
      ? Position.Right
      : Position.Left
    : dy > 0
      ? Position.Bottom
      : Position.Top
  const insetX = Math.min(14, hw / 2)
  const insetY = Math.min(14, hh / 2)
  return {
    x: horizontal
      ? origin.x + dx * scale
      : Math.max(rect.x + insetX, Math.min(rect.x + rect.width - insetX, origin.x + dx * scale)),
    y: horizontal
      ? Math.max(rect.y + insetY, Math.min(rect.y + rect.height - insetY, origin.y + dy * scale))
      : origin.y + dy * scale,
    position,
  }
}

// Group frames are derived on each render rather than stored in useNodesState.
// Supply their measurements and handle bounds explicitly: React Flow otherwise
// clears handleBounds when a new node object has no `measured`, hiding its edges.
// These invisible bounds only initialize React Flow; planAnchors determines the
// actual continuous attachment points on the node and group perimeters.
export function groupFrameDimensions(width: number, height: number) {
  return {
    width,
    height,
    measured: { width, height },
    handles: [
      {
        id: 'surface',
        type: 'source' as const,
        position: Position.Top,
        x: -8,
        y: -8,
        width: width + 16,
        height: height + 16,
      },
      {
        id: 'target',
        type: 'target' as const,
        position: Position.Top,
        x: width / 2,
        y: 0,
        width: 1,
        height: 1,
      },
    ],
  }
}

export function planAnchors(
  rectangles: Map<string, Rect>,
  edges: { id: string; a: string; b: string }[],
) {
  const result = new Map<string, Anchors>()
  const groups = new Map<string, { id: string; anchor: Anchor; rect: Rect }[]>()
  for (const edge of edges) {
    const a = rectangles.get(edge.a),
      b = rectangles.get(edge.b)
    if (!a || !b) continue
    const points = { source: boundary(a, center(b), 1), target: boundary(b, center(a), -1) }
    result.set(edge.id, points)
    for (const [node, rect, anchor] of [
      [edge.a, a, points.source],
      [edge.b, b, points.target],
    ] as const) {
      const key = `${node}/${anchor.position}`
      const entries = groups.get(key) ?? []
      entries.push({ id: edge.id, anchor, rect })
      groups.set(key, entries)
    }
  }
  // Fan neighboring edges apart while preserving angular order and keeping
  // attachment points away from rounded corners. Dense sides share the space.
  for (const entries of groups.values()) {
    const first = entries[0]
    if (!first || entries.length < 2) continue
    const horizontal =
      first.anchor.position === Position.Top || first.anchor.position === Position.Bottom
    const axis = horizontal ? 'x' : 'y'
    const length = horizontal ? first.rect.width : first.rect.height
    const inset = Math.min(14, length / 4)
    const lower = first.rect[axis] + inset
    const upper = first.rect[axis] + length - inset
    const gap = Math.min(16, (upper - lower) / (entries.length - 1))
    entries.sort((a, b) => a.anchor[axis] - b.anchor[axis] || a.id.localeCompare(b.id))
    for (let i = 0; i < entries.length; i++) {
      entries[i].anchor[axis] = Math.max(
        entries[i].anchor[axis],
        i ? entries[i - 1].anchor[axis] + gap : lower,
      )
    }
    for (let i = entries.length - 1; i >= 0; i--) {
      entries[i].anchor[axis] = Math.min(
        entries[i].anchor[axis],
        i === entries.length - 1 ? upper : entries[i + 1].anchor[axis] - gap,
      )
    }
  }
  return result
}
