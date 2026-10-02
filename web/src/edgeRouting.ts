import { getBezierPath } from '@xyflow/react'
import type { Anchors, Rect } from './edgeGeometry'

export type LineStyle = 'line' | 'bezier'

type Point = { x: number; y: number }
type Link = { id: string; a: string; b: string }
export type Route = { path: string; labelX: number; labelY: number; points: Point[] }
const distance = (a: Point, b: Point) => Math.hypot(a.x - b.x, a.y - b.y)
const interpolate = (a: Point, b: Point, t: number): Point => ({
  x: a.x + (b.x - a.x) * t,
  y: a.y + (b.y - a.y) * t,
})
function separation(p: Point, a: Point, b: Point) {
  const dx = b.x - a.x,
    dy = b.y - a.y
  const t = Math.max(
    0,
    Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / (dx * dx + dy * dy || 1)),
  )
  return distance(p, interpolate(a, b, t))
}

// Test the interior, allowing segments to touch an obstacle's perimeter.
function blocked(a: Point, b: Point, r: Rect) {
  let enter = 0,
    leave = 1
  for (const [start, delta, lower, upper] of [
    [a.x, b.x - a.x, r.x + 0.01, r.x + r.width - 0.01],
    [a.y, b.y - a.y, r.y + 0.01, r.y + r.height - 0.01],
  ]) {
    if (Math.abs(delta) < 0.000001) {
      if (start <= lower || start >= upper) return false
    } else {
      const t1 = (lower - start) / delta,
        t2 = (upper - start) / delta
      enter = Math.max(enter, Math.min(t1, t2))
      leave = Math.min(leave, Math.max(t1, t2))
      if (enter >= leave) return false
    }
  }
  return enter < leave
}

function makeRoute(vertices: Point[]): Route {
  const first = vertices[0],
    last = vertices[vertices.length - 1]
  let path = `M ${first.x},${first.y}`
  const samples: Point[] = [first]
  for (let i = 1; i < vertices.length - 1; i++) {
    const previous = vertices[i - 1],
      corner = vertices[i],
      next = vertices[i + 1]
    const radius = Math.min(8, distance(previous, corner) / 3, distance(corner, next) / 3)
    const before = interpolate(corner, previous, radius / (distance(corner, previous) || 1))
    const after = interpolate(corner, next, radius / (distance(corner, next) || 1))
    path += ` L ${before.x},${before.y} Q ${corner.x},${corner.y} ${after.x},${after.y}`
    samples.push(before)
    for (let j = 1; j <= 8; j++) {
      const t = j / 8,
        u = 1 - t
      samples.push({
        x: u * u * before.x + 2 * u * t * corner.x + t * t * after.x,
        y: u * u * before.y + 2 * u * t * corner.y + t * t * after.y,
      })
    }
  }
  path += ` L ${last.x},${last.y}`
  samples.push(last)
  const lengths = samples.slice(1).map((p, i) => distance(samples[i], p))
  const total = lengths.reduce((sum, n) => sum + n, 0)
  const points = Array.from({ length: 25 }, (_, i) => {
    let remaining = (total * i) / 24
    for (let j = 0; j < lengths.length; j++) {
      if (remaining <= lengths[j])
        return interpolate(samples[j], samples[j + 1], remaining / (lengths[j] || 1))
      remaining -= lengths[j]
    }
    return last
  })
  return { path, points, labelX: points[12].x, labelY: points[12].y }
}

export function bezierRoute(pair: Anchors): Route {
  const [path, labelX, labelY] = getBezierPath({
    sourceX: pair.source.x,
    sourceY: pair.source.y,
    sourcePosition: pair.source.position,
    targetX: pair.target.x,
    targetY: pair.target.y,
    targetPosition: pair.target.position,
  })
  const [ax, ay, cx, cy, dx, dy, bx, by] = path
    .match(/[-+]?(?:\d*\.\d+|\d+)(?:[eE][-+]?\d+)?/g)!
    .map(Number)
  const points = Array.from({ length: 25 }, (_, i) => {
    const t = i / 24,
      u = 1 - t
    return {
      x: u * u * u * ax + 3 * u * u * t * cx + 3 * u * t * t * dx + t * t * t * bx,
      y: u * u * u * ay + 3 * u * u * t * cy + 3 * u * t * t * dy + t * t * t * by,
    }
  })
  return { path, labelX, labelY, points }
}

// Use a straight segment whenever it is clear. For an actual card obstruction,
// find the shortest visible path around padded card corners. Crossings never
// justify bending an otherwise unobstructed edge.
export function planRoutes(
  rectangles: Map<string, Rect>,
  edges: Link[],
  anchors: Map<string, Anchors>,
  fast = false,
  style: LineStyle = 'line',
) {
  const routes = new Map<string, Route>()
  for (const edge of [...edges].sort((a, b) => a.id.localeCompare(b.id))) {
    const pair = anchors.get(edge.id)
    if (!pair) continue
    if (style === 'bezier') {
      routes.set(edge.id, bezierRoute(pair))
      continue
    }
    const a = { x: pair.source.x, y: pair.source.y },
      b = { x: pair.target.x, y: pair.target.y }
    const obstacles = [...rectangles].map(([id, r]) => {
      const padding = id === edge.a || id === edge.b ? 0 : 12
      return {
        x: r.x - padding,
        y: r.y - padding,
        width: r.width + padding * 2,
        height: r.height + padding * 2,
      }
    })
    const clear = (p: Point, q: Point) => !obstacles.some((r) => blocked(p, q, r))
    if (fast || clear(a, b)) {
      routes.set(edge.id, makeRoute([a, b]))
      continue
    }
    const vertices = [
      a,
      b,
      ...obstacles.flatMap((r) => [
        { x: r.x, y: r.y },
        { x: r.x + r.width, y: r.y },
        { x: r.x + r.width, y: r.y + r.height },
        { x: r.x, y: r.y + r.height },
      ]),
    ]
    const costs = vertices.map(() => Infinity),
      previous = vertices.map(() => -1)
    const visited = new Set<number>()
    costs[0] = 0
    for (let step = 0; step < vertices.length; step++) {
      let current = -1
      for (let i = 0; i < vertices.length; i++)
        if (!visited.has(i) && (current < 0 || costs[i] < costs[current])) current = i
      if (current < 0 || !Number.isFinite(costs[current]) || current === 1) break
      visited.add(current)
      for (let i = 0; i < vertices.length; i++) {
        if (visited.has(i) || !clear(vertices[current], vertices[i])) continue
        const cost = costs[current] + distance(vertices[current], vertices[i]) + 8
        if (cost < costs[i]) {
          costs[i] = cost
          previous[i] = current
        }
      }
    }
    const chain: Point[] = []
    let index = 1
    if (Number.isFinite(costs[1])) {
      while (index >= 0) {
        chain.unshift(vertices[index])
        index = previous[index]
      }
    }
    routes.set(edge.id, makeRoute(chain.length > 1 ? chain : [a, b]))
  }
  return routes
}

// Labels remain on their own curve; spread them along it instead of stacking
// every label at its midpoint. Focused links get first choice.
export function planLabels(
  routes: Map<string, Route>,
  rectangles: Map<string, Rect>,
  priority: Set<string>,
  labelScale = 1,
) {
  const width = 188 * labelScale
  const height = 28 * labelScale
  const labels: Rect[] = []
  const result = new Map<string, Point>()
  const entries = [...routes].sort(
    ([a], [b]) => Number(priority.has(b)) - Number(priority.has(a)) || a.localeCompare(b),
  )
  for (const [id, route] of entries) {
    let best = route.points[12],
      score = Infinity
    for (const index of [12, 9, 15, 6, 18, 4, 20]) {
      const p = route.points[index],
        box = { x: p.x - width / 2, y: p.y - height / 2, width, height }
      const overlaps = (r: Rect) =>
        box.x < r.x + r.width &&
        box.x + box.width > r.x &&
        box.y < r.y + r.height &&
        box.y + box.height > r.y
      let cost = Math.abs(index - 12) * 2
      for (const r of rectangles.values()) if (overlaps(r)) cost += 10000
      for (const r of labels) if (overlaps(r)) cost += 3000
      for (const [otherID, other] of routes)
        if (otherID !== id) {
          for (let i = 1; i < other.points.length; i++) {
            if (separation(p, other.points[i - 1], other.points[i]) < 22 * labelScale) cost += 30
          }
        }
      if (cost < score) {
        best = p
        score = cost
      }
    }
    result.set(id, best)
    labels.push({ x: best.x - width / 2, y: best.y - height / 2, width, height })
  }
  return result
}
