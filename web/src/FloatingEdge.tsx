import {
  BaseEdge,
  getBezierPath,
  Position,
  type Edge,
  type EdgeProps,
  type ConnectionLineComponentProps,
  type InternalNode,
} from '@xyflow/react'
import { boundary, center, type Anchors, type Rect } from './edgeGeometry'

export type FloatingFlowEdge = Edge<{ anchors: Anchors }, 'floating'>
export function FloatingEdge(props: EdgeProps<FloatingFlowEdge>) {
  if (!props.data?.anchors) return null
  const { source, target } = props.data.anchors
  const [path, labelX, labelY] = getBezierPath({
    sourceX: source.x,
    sourceY: source.y,
    sourcePosition: source.position,
    targetX: target.x,
    targetY: target.y,
    targetPosition: target.position,
  })
  return (
    <BaseEdge
      id={props.id}
      path={path}
      labelX={labelX}
      labelY={labelY}
      label={props.label}
      labelStyle={props.labelStyle}
      labelBgStyle={props.labelBgStyle}
      labelBgPadding={props.labelBgPadding}
      labelBgBorderRadius={props.labelBgBorderRadius}
      style={props.style}
      interactionWidth={26}
    />
  )
}
const rectOf = (node: InternalNode): Rect => ({
  ...node.internals.positionAbsolute,
  width: node.measured.width ?? 198,
  height: node.measured.height ?? 64,
})
const opposite = {
  [Position.Top]: Position.Bottom,
  [Position.Bottom]: Position.Top,
  [Position.Left]: Position.Right,
  [Position.Right]: Position.Left,
}
export function FloatingConnection({
  fromNode,
  toNode,
  toX,
  toY,
  connectionStatus,
}: ConnectionLineComponentProps) {
  const from = rectOf(fromNode)
  const to = toNode ? rectOf(toNode) : undefined
  const source = boundary(from, to ? center(to) : { x: toX, y: toY })
  const target = to
    ? boundary(to, center(from), -1)
    : { x: toX, y: toY, position: opposite[source.position] }
  const [path] = getBezierPath({
    sourceX: source.x,
    sourceY: source.y,
    sourcePosition: source.position,
    targetX: target.x,
    targetY: target.y,
    targetPosition: target.position,
  })
  return (
    <g>
      <path
        d={path}
        fill="none"
        stroke={connectionStatus === 'invalid' ? '#b84646' : '#237561'}
        strokeWidth={2}
        strokeDasharray="6 4"
      />
      <circle cx={target.x} cy={target.y} r={4} fill="#237561" />
    </g>
  )
}
