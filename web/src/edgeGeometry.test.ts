import { describe, expect, it } from 'vitest'
import { Position } from '@xyflow/react'
import { boundary, planAnchors, type Rect } from './edgeGeometry'

const card: Rect = { x: 100, y: 200, width: 200, height: 80 }
describe('continuous perimeter connections', () => {
  it('attaches to all four sides and moves continuously along a side', () => {
    expect(boundary(card, { x: 500, y: 240 })).toEqual({ x: 300, y: 240, position: Position.Right })
    expect(boundary(card, { x: 0, y: 240 })).toEqual({ x: 100, y: 240, position: Position.Left })
    expect(boundary(card, { x: 200, y: 0 })).toEqual({ x: 200, y: 200, position: Position.Top })
    expect(boundary(card, { x: 200, y: 500 })).toEqual({
      x: 200,
      y: 280,
      position: Position.Bottom,
    })
    expect(boundary(card, { x: 500, y: 270 }).y).toBe(250)
    expect(boundary(card, { x: 500, y: 285 }).y).toBe(255)
  })
  it('keeps dense attachments separated, inside the card, and stable across edge order', () => {
    const rectangles = new Map([['center', card]])
    const edges = Array.from({ length: 12 }, (_, i) => {
      rectangles.set(`peer${i}`, { x: 700, y: 200 + i, width: 200, height: 80 })
      return { id: `edge${i}`, a: 'center', b: `peer${i}` }
    })
    const planned = planAnchors(rectangles, edges)
    const reversed = planAnchors(rectangles, [...edges].reverse())
    const ys = edges.map(({ id }) => {
      expect(planned.get(id)).toEqual(reversed.get(id))
      const source = planned.get(id)!.source
      expect(source.position).toBe(Position.Right)
      expect(source.x).toBe(300)
      expect(source.y).toBeGreaterThanOrEqual(214)
      expect(source.y).toBeLessThanOrEqual(266)
      return source.y
    })
    expect(new Set(ys).size).toBe(12)
    expect(ys).toEqual([...ys].sort((a, b) => a - b))
  })
  it('replans after a move and handles coincident centers and missing nodes', () => {
    const rectangles = new Map([
      ['a', card],
      ['b', { ...card, x: 700 }],
    ])
    const edges = [{ id: 'edge', a: 'a', b: 'b' }]
    expect(planAnchors(rectangles, edges).get('edge')!.source.position).toBe(Position.Right)
    rectangles.set('b', { ...card, y: 700 })
    expect(planAnchors(rectangles, edges).get('edge')!.source.position).toBe(Position.Bottom)
    rectangles.set('b', card)
    const pair = planAnchors(rectangles, edges).get('edge')!
    expect(pair.source).toEqual({ x: 300, y: 240, position: Position.Right })
    expect(pair.target).toEqual({ x: 100, y: 240, position: Position.Left })
    rectangles.delete('b')
    expect(planAnchors(rectangles, edges).size).toBe(0)
  })
})
