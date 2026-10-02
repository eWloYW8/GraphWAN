import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  Position,
  type Edge,
  type EdgeProps,
  type ConnectionLineComponentProps,
  type InternalNode,
} from '@xyflow/react'
import { boundary, center, type Anchors, type Rect } from './edgeGeometry'
import type { Route } from './edgeRouting'

export type FloatingFlowEdge = Edge<
  {
    anchors: Anchors
    route: Route
    labelPoint: { x: number; y: number }
    labelScale?: number
    open?: () => void
    focused: boolean
    dimmed: boolean
  },
  'floating'
>
export function FloatingEdge(props: EdgeProps<FloatingFlowEdge>) {
  if (!props.data?.anchors) return null
  const { route, labelPoint, labelScale = 1, focused, dimmed } = props.data
  if (!route) return null
  return (
    <>
      <BaseEdge id={props.id} path={route.path} style={props.style} interactionWidth={26} />
      {!dimmed && props.label && (
        <EdgeLabelRenderer>
          <div
            className={`graph-edge-label nodrag nopan ${focused ? 'focused' : ''}`}
            style={{
              transform: `translate(-50%, -50%) translate(${labelPoint.x}px, ${labelPoint.y}px) scale(${labelScale})`,
              zIndex: focused ? 1000 : 10,
              pointerEvents: props.data.open ? 'all' : 'none',
              cursor: props.data.open ? 'pointer' : undefined,
            }}
            role={props.data.open ? 'button' : undefined}
            tabIndex={props.data.open ? 0 : undefined}
            onClick={(event) => {
              event.stopPropagation()
              props.data?.open?.()
            }}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault()
                event.stopPropagation()
                props.data?.open?.()
              }
            }}
            title={typeof props.label === 'string' ? props.label : undefined}
          >
            {props.label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
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
  lineStyle = 'bezier',
}: ConnectionLineComponentProps & { lineStyle?: 'line' | 'bezier' }) {
  const from = rectOf(fromNode)
  const to = toNode ? rectOf(toNode) : undefined
  const source = boundary(from, to ? center(to) : { x: toX, y: toY })
  const target = to
    ? boundary(to, center(from), -1)
    : { x: toX, y: toY, position: opposite[source.position] }
  const [curve] = getBezierPath({
    sourceX: source.x,
    sourceY: source.y,
    sourcePosition: source.position,
    targetX: target.x,
    targetY: target.y,
    targetPosition: target.position,
  })
  const path = lineStyle === 'line' ? `M ${source.x},${source.y} L ${target.x},${target.y}` : curve
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

export function LineConnection(props: ConnectionLineComponentProps) {
  return <FloatingConnection {...props} lineStyle="line" />
}
