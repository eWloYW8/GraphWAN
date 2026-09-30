import { describe, expect, it } from 'vitest'
import { planAnchors, type Rect } from './edgeGeometry'
import { planRoutes, planLabels } from './edgeRouting'

describe('graph-wide routing', () => {
  it('offers Bezier curves with labels and samples on the same path', () => {
    const rects = new Map<string, Rect>([
      ['a', { x: 0, y: 0, width: 100, height: 60 }],
      ['b', { x: 400, y: 200, width: 100, height: 60 }],
    ])
    const edges = [{ id: 'ab', a: 'a', b: 'b' }]
    const anchors = planAnchors(rects, edges)
    const route = planRoutes(rects, edges, anchors, false, 'bezier').get('ab')!
    expect(route.path).toContain('C')
    expect(route.points.every((p) => Number.isFinite(p.x) && Number.isFinite(p.y))).toBe(true)
    expect(route.points[0]).toEqual({
      x: anchors.get('ab')!.source.x,
      y: anchors.get('ab')!.source.y,
    })
    expect(route.points[24]).toEqual({
      x: anchors.get('ab')!.target.x,
      y: anchors.get('ab')!.target.y,
    })
    expect(route.points[12].x).toBeCloseTo(route.labelX)
    expect(route.points[12].y).toBeCloseTo(route.labelY)
  })
  it('keeps unobstructed crossing links straight instead of bending to avoid crossings', () => {
    const rects = new Map<string, Rect>([
      ['a', { x: 0, y: 0, width: 100, height: 60 }],
      ['b', { x: 500, y: 300, width: 100, height: 60 }],
      ['c', { x: 0, y: 300, width: 100, height: 60 }],
      ['d', { x: 500, y: 0, width: 100, height: 60 }],
    ])
    const edges = [
      { id: 'ab', a: 'a', b: 'b' },
      { id: 'cd', a: 'c', b: 'd' },
    ]
    const anchors = planAnchors(rects, edges)
    for (const route of planRoutes(rects, edges, anchors).values()) {
      const start = route.points[0],
        end = route.points[24]
      for (const p of route.points) {
        expect(
          Math.abs((p.x - start.x) * (end.y - start.y) - (p.y - start.y) * (end.x - start.x)),
        ).toBeLessThan(0.00001)
      }
    }
  })
  it('routes around intervening cards and is stable when the edge array changes order', () => {
    const rects = new Map<string, Rect>([
      ['a', { x: 0, y: 0, width: 100, height: 60 }],
      ['b', { x: 500, y: 0, width: 100, height: 60 }],
      ['obstacle', { x: 250, y: 0, width: 100, height: 60 }],
      ['c', { x: 0, y: 220, width: 100, height: 60 }],
    ])
    const edges = [
      { id: 'ab', a: 'a', b: 'b' },
      { id: 'cb', a: 'c', b: 'b' },
    ]
    const anchors = planAnchors(rects, edges)
    const routes = planRoutes(rects, edges, anchors)
    expect(routes).toEqual(planRoutes(rects, [...edges].reverse(), anchors))
    const obstacle = rects.get('obstacle')!
    for (const p of routes.get('ab')!.points) {
      expect(
        p.x > obstacle.x &&
          p.x < obstacle.x + obstacle.width &&
          p.y > obstacle.y &&
          p.y < obstacle.y + obstacle.height,
      ).toBe(false)
    }
    expect(routes.get('ab')!.points[0]).toEqual({
      x: anchors.get('ab')!.source.x,
      y: anchors.get('ab')!.source.y,
    })
  })
  it('separates colliding labels along their curves and prioritizes focused edges', () => {
    const points = Array.from({ length: 25 }, (_, i) => ({ x: i * 30, y: 0 }))
    const routes = new Map(
      ['a', 'b'].map((id) => [id, { path: '', labelX: 360, labelY: 0, points }]),
    )
    const labels = planLabels(routes, new Map(), new Set(['b']))
    expect(labels.get('b')).toEqual({ x: 360, y: 0 })
    expect(Math.abs(labels.get('a')!.x - labels.get('b')!.x)).toBeGreaterThanOrEqual(188)
  })
})
