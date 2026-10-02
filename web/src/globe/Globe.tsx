import { useEffect, useRef, useState } from 'react'
import * as THREE from 'three'
import { OrbitControls } from 'three/addons/controls/OrbitControls.js'
import countries from './assets/countries.json'
import type {
  GlobeGroup,
  GlobeGroupLink,
  GlobeNode,
  GlobeEdge,
  Selection,
  Appearance,
} from './types'

export type ViewCommand = { kind: 'reset' | 'in' | 'out' | 'focus'; id?: string; serial: number }
const teal = '#69e8c0'
const amber = '#ffd184'

function position(latitude: number, longitude: number) {
  const lat = THREE.MathUtils.degToRad(latitude)
  const lon = THREE.MathUtils.degToRad(longitude)
  return new THREE.Vector3(
    Math.cos(lat) * Math.cos(lon),
    Math.sin(lat),
    -Math.cos(lat) * Math.sin(lon),
  )
}

function earthTexture() {
  const canvas = document.createElement('canvas')
  canvas.width = 4096
  canvas.height = 2048
  const ctx = canvas.getContext('2d')!
  ctx.fillStyle = '#0b2131'
  ctx.fillRect(0, 0, canvas.width, canvas.height)
  ctx.strokeStyle = '#193748'
  ctx.lineWidth = 1
  for (let lon = -180; lon <= 180; lon += 15) {
    const x = ((lon + 180) / 360) * canvas.width
    ctx.beginPath()
    ctx.moveTo(x, 0)
    ctx.lineTo(x, canvas.height)
    ctx.stroke()
  }
  for (let lat = -75; lat <= 75; lat += 15) {
    const y = ((90 - lat) / 180) * canvas.height
    ctx.beginPath()
    ctx.moveTo(0, y)
    ctx.lineTo(canvas.width, y)
    ctx.stroke()
  }
  ctx.fillStyle = '#255163'
  ctx.strokeStyle = '#477584'
  ctx.lineWidth = 1.25
  for (const polygon of countries) {
    ctx.beginPath()
    for (const ring of polygon) {
      ring.forEach(([lon, lat], i) => {
        const x = ((lon + 180) / 360) * canvas.width
        const y = ((90 - lat) / 180) * canvas.height
        if (!i) ctx.moveTo(x, y)
        else ctx.lineTo(x, y)
      })
      ctx.closePath()
    }
    ctx.fill('evenodd')
    ctx.stroke()
  }
  const texture = new THREE.CanvasTexture(canvas)
  texture.colorSpace = THREE.SRGBColorSpace
  return texture
}

// Great-circle interpolation plus a radial lift keeps the entire curve above
// the globe. The fallback axis also handles identical and antipodal endpoints.
function arc(a: THREE.Vector3, b: THREE.Vector3) {
  const start = a.clone().normalize()
  const end = b.clone().normalize()
  const angle = Math.acos(THREE.MathUtils.clamp(start.dot(end), -1, 1))
  let axis = new THREE.Vector3().crossVectors(start, end)
  if (axis.lengthSq() < 1e-10) {
    axis = new THREE.Vector3().crossVectors(
      start,
      Math.abs(start.y) < 0.9 ? new THREE.Vector3(0, 1, 0) : new THREE.Vector3(1, 0, 0),
    )
  }
  axis.normalize()
  return Array.from({ length: 97 }, (_, i) => {
    const t = i / 96
    return start
      .clone()
      .applyAxisAngle(axis, angle * t)
      .multiplyScalar(1.027 + Math.sin(Math.PI * t) * (0.045 + angle * 0.2))
  })
}

export default function Globe({
  nodes,
  groups = [],
  groupLinks = [],
  enterGroup,
  edges,
  selection,
  select,
  rotating,
  labels,
  command,
  appearance,
  connect,
}: {
  nodes: GlobeNode[]
  groups?: GlobeGroup[]
  groupLinks?: GlobeGroupLink[]
  enterGroup?: (kind: 'group' | 'link', id: string) => void
  edges: GlobeEdge[]
  selection: Selection
  select: (selection: Selection, multiple?: boolean) => void
  rotating: boolean
  labels: boolean
  command: ViewCommand
  appearance?: Appearance
  connect?: (source: string, target: string) => void
}) {
  const host = useRef<HTMLDivElement>(null)
  const [error, setError] = useState('')
  const options = useRef({
    nodes,
    selection,
    select,
    rotating,
    labels,
    appearance,
    connect,
    groups,
    groupLinks,
    enterGroup,
  })
  options.current = {
    nodes,
    selection,
    select,
    rotating,
    labels,
    appearance,
    connect,
    groups,
    groupLinks,
    enterGroup,
  }
  const cameraPosition = useRef<THREE.Vector3 | null>(null)
  const cameraAspect = useRef(1)
  // Live status updates must never rebuild geometry or reset the orbit position.
  const geometryKey = JSON.stringify([
    nodes.map(({ id, latitude, longitude }) => [id, latitude, longitude]),
    edges,
  ])
  const api = useRef<{ refresh: () => void; command: (command: ViewCommand) => void } | null>(null)

  useEffect(() => {
    const element = host.current!
    let renderer: THREE.WebGLRenderer
    try {
      renderer = new THREE.WebGLRenderer({
        antialias: true,
        alpha: true,
        powerPreference: 'low-power',
      })
    } catch {
      setError('WebGL 2 is unavailable. Enable hardware acceleration or use Line / Bezier view.')
      return
    }
    setError('')
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 1.75))
    renderer.outputColorSpace = THREE.SRGBColorSpace
    element.appendChild(renderer.domElement)
    renderer.domElement.setAttribute(
      'aria-label',
      'Interactive globe. Drag to rotate, scroll to zoom, select a node for details.',
    )
    renderer.domElement.setAttribute('role', 'img')
    // Screen-space leaders span the globe viewport and the two label gutters.
    // They do not participate in raycasting or intercept orbit gestures.
    const svgNS = 'http://www.w3.org/2000/svg'
    const leaders = document.createElementNS(svgNS, 'svg')
    leaders.classList.add('globe-label-leaders')
    leaders.setAttribute('aria-hidden', 'true')
    element.appendChild(leaders)
    const groupLayer = document.createElementNS(svgNS, 'svg')
    groupLayer.classList.add('globe-group-layer')
    element.appendChild(groupLayer)
    const makeColumn = (side: 'left' | 'right') => {
      const viewport = document.createElement('div')
      viewport.className = 'globe-label-column'
      viewport.dataset.side = side
      viewport.setAttribute('role', 'group')
      viewport.setAttribute('aria-label', `${side === 'left' ? 'Left' : 'Right'} globe nodes`)
      const content = document.createElement('div')
      content.className = 'globe-label-content'
      viewport.appendChild(content)
      element.appendChild(viewport)
      return { viewport, content }
    }
    const columns = { left: makeColumn('left'), right: makeColumn('right') }
    const gutterFor = (canvasWidth: number) => Math.min(180, canvasWidth * 0.23)
    const scene = new THREE.Scene()
    const camera = new THREE.PerspectiveCamera(42, cameraAspect.current, 0.1, 100)
    const home = position(24, 48).multiplyScalar(3.75)
    camera.position.copy(cameraPosition.current || home)
    const controls = new OrbitControls(camera, renderer.domElement)
    controls.enablePan = false
    controls.enableDamping = false
    controls.minDistance = 1.65
    controls.maxDistance = 9
    controls.autoRotateSpeed = 0.35
    controls.rotateSpeed = 0.65
    controls.update()
    const texture = earthTexture()
    texture.anisotropy = Math.min(8, renderer.capabilities.getMaxAnisotropy())
    const earth = new THREE.Mesh(
      new THREE.SphereGeometry(1, 96, 64),
      new THREE.MeshBasicMaterial({ map: texture }),
    )
    scene.add(earth)
    // A subtle additive atmospheric rim, with no post-processing passes.
    scene.add(
      new THREE.Mesh(
        new THREE.SphereGeometry(1.025, 64, 48),
        new THREE.ShaderMaterial({
          uniforms: { tint: { value: new THREE.Color('#348dbc') } },
          vertexShader: `varying vec3 n; varying vec3 view; void main() { vec4 p=modelViewMatrix*vec4(position,1.0); n=normalize(normalMatrix*normal); view=normalize(-p.xyz); gl_Position=projectionMatrix*p; }`,
          fragmentShader: `uniform vec3 tint; varying vec3 n; varying vec3 view; void main() { float rim=pow(1.0-abs(dot(normalize(n),normalize(view))),4.0); gl_FragColor=vec4(tint,rim*0.36); }`,
          transparent: true,
          blending: THREE.AdditiveBlending,
          depthWrite: false,
        }),
      ),
    )

    const picks: THREE.Object3D[] = []
    type Marker = {
      mesh: THREE.Mesh<THREE.SphereGeometry, THREE.MeshBasicMaterial>
      halo: THREE.Mesh<THREE.RingGeometry, THREE.MeshBasicMaterial>
      point: THREE.Vector3
      label: HTMLButtonElement
      leader: SVGPathElement
      side?: 'left' | 'right'
    }
    const markers = new Map<string, Marker>()
    const groups: GlobeNode[][] = []
    for (const node of [...nodes].sort((a, b) => a.id.localeCompare(b.id))) {
      const group = groups.find(
        (g) =>
          position(g[0].latitude, g[0].longitude).distanceTo(
            position(node.latitude, node.longitude),
          ) < 0.008,
      )
      if (group) group.push(node)
      else groups.push([node])
    }
    for (const group of groups) {
      group.forEach((node, index) => {
        const original = position(node.latitude, node.longitude)
        const east = new THREE.Vector3()
          .crossVectors(new THREE.Vector3(0, 1, 0), original)
          .normalize()
        if (east.lengthSq() < 0.1) east.set(1, 0, 0)
        const north = new THREE.Vector3().crossVectors(original, east).normalize()
        const offset = index - (group.length - 1) / 2
        const point = original
          .clone()
          .addScaledVector(east, offset * 0.075)
          .addScaledVector(north, Math.abs(offset) * 0.015)
          .normalize()
          .multiplyScalar(1.027)
        const mesh = new THREE.Mesh(
          new THREE.SphereGeometry(0.01, 16, 12),
          new THREE.MeshBasicMaterial({ color: teal }),
        )
        mesh.position.copy(point)
        mesh.userData.selection = { type: 'node', id: node.id }
        scene.add(mesh)
        picks.push(mesh)
        const halo = new THREE.Mesh(
          new THREE.RingGeometry(0.017, 0.022, 32),
          new THREE.MeshBasicMaterial({
            color: teal,
            transparent: true,
            opacity: 0.55,
            side: THREE.DoubleSide,
          }),
        )
        halo.position.copy(point)
        halo.quaternion.setFromUnitVectors(new THREE.Vector3(0, 0, 1), original)
        scene.add(halo)
        if (group.length > 1) {
          scene.add(
            new THREE.Line(
              new THREE.BufferGeometry().setFromPoints(
                arc(original.clone().multiplyScalar(1.003), point).map((p, i, arr) =>
                  p.normalize().multiplyScalar(1.004 + (i / (arr.length - 1)) * 0.023),
                ),
              ),
              new THREE.LineBasicMaterial({ color: '#8ba9af', transparent: true, opacity: 0.7 }),
            ),
          )
        }
        const label = document.createElement('button')
        label.className = 'globe-node-label'
        label.textContent = node.name
        label.title = `${node.name} · ${node.city} · ${node.ip}`
        label.onclick = (event) =>
          options.current.select({ type: 'node', id: node.id }, event.ctrlKey || event.metaKey)
        const leader = document.createElementNS(svgNS, 'path')
        leader.classList.add('globe-label-leader')
        leaders.appendChild(leader)
        label.onpointerenter = () => {
          leader.dataset.hover = 'true'
        }
        label.onpointerleave = () => {
          leader.dataset.hover = 'false'
        }
        columns.left.content.appendChild(label)
        markers.set(node.id, { mesh, halo, point, label, leader })
      })
    }
    const lines = edges.flatMap((edge) => {
      const a = markers.get(edge.a),
        b = markers.get(edge.b)
      if (!a || !b) return []
      const points = arc(a.point, b.point)
      const mesh = new THREE.Line(
        new THREE.BufferGeometry().setFromPoints(points),
        new THREE.LineDashedMaterial({
          color: teal,
          transparent: true,
          opacity: 0.72,
          dashSize: 100,
          gapSize: 0,
          depthWrite: false,
        }),
      )
      mesh.computeLineDistances()
      mesh.userData.selection = { type: 'edge', id: edge.id }
      scene.add(mesh)
      picks.push(mesh)
      return [{ edge, mesh, midpoint: points[Math.floor(points.length / 2)] }]
    })

    let width = cameraAspect.current,
      height = 1,
      frame = 0,
      disposed = false,
      last = 0
    let fly: { from: THREE.Vector3; to: THREE.Vector3; start: number } | null = null
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
    const project = new THREE.Vector3()
    const updateLabels = () => {
      const gutter = gutterFor(width)
      const globeWidth = width - gutter * 2
      const top = Math.min(96, height * 0.2)
      const bottom = Math.min(118, height * 0.24)
      const available = Math.max(1, height - top - bottom)
      const labelHeight = 26
      const step = labelHeight + 8
      const visibleNodes: { id: string; marker: Marker; x: number; y: number }[] = []
      for (const [id, marker] of markers) {
        const { label, point, leader } = marker
        project.copy(point).project(camera)
        const visible =
          options.current.labels &&
          point.dot(camera.position) > 1.04 &&
          Math.abs(project.x) < 0.98 &&
          Math.abs(project.y) < 0.96
        label.hidden = !visible
        leader.style.display = 'none'
        if (!visible) continue
        const x = gutter + (project.x * 0.5 + 0.5) * globeWidth,
          y = (-project.y * 0.5 + 0.5) * height
        visibleNodes.push({ id, marker, x, y })
      }
      groupLayer.setAttribute('viewBox', `0 0 ${width} ${height}`)
      groupLayer.replaceChildren()
      const boxes = new Map<string, { x: number; y: number; width: number; height: number }>()
      for (const group of options.current.groups) {
        const points = visibleNodes.filter((n) => group.members.includes(n.id))
        if (!points.length) continue
        const x = Math.min(...points.map((p) => p.x)) - 24,
          y = Math.min(...points.map((p) => p.y)) - 44
        const box = {
          x,
          y,
          width: Math.max(...points.map((p) => p.x)) - x + 24,
          height: Math.max(...points.map((p) => p.y)) - y + 24,
        }
        boxes.set(group.id, box)
        const shape = document.createElementNS(svgNS, 'rect')
        for (const [key, value] of Object.entries(box)) shape.setAttribute(key, String(value))
        shape.setAttribute('rx', '16')
        shape.setAttribute('class', 'globe-mesh-boundary')
        shape.addEventListener('click', (event) => {
          const rect = groupLayer.getBoundingClientRect(),
            px = event.clientX - rect.left,
            py = event.clientY - rect.top
          const node = visibleNodes.find((n) => Math.hypot(n.x - px, n.y - py) < 14)
          if (node)
            options.current.select({ type: 'node', id: node.id }, event.ctrlKey || event.metaKey)
          else options.current.enterGroup?.('group', group.id)
        })
        groupLayer.appendChild(shape)
        const label = document.createElementNS(svgNS, 'text')
        label.setAttribute('x', String(x + 10))
        label.setAttribute('y', String(y + 19))
        label.setAttribute('class', 'globe-mesh-caption')
        label.textContent = `${group.name} · ${group.summary}`
        label.addEventListener('click', () => options.current.enterGroup?.('group', group.id))
        groupLayer.appendChild(label)
      }
      for (const link of options.current.groupLinks) {
        const box = boxes.get(link.group),
          source = visibleNodes.find((n) => n.id === link.node)
        if (!box || !source) continue
        const cx = box.x + box.width / 2,
          cy = box.y + box.height / 2,
          dx = source.x - cx,
          dy = source.y - cy
        const scale =
          1 / Math.max(Math.abs(dx) / (box.width / 2), Math.abs(dy) / (box.height / 2), 1)
        const x = cx + dx * scale,
          y = cy + dy * scale
        const path = document.createElementNS(svgNS, 'path')
        path.setAttribute(
          'd',
          `M${source.x},${source.y} Q${(source.x + x) / 2},${Math.min(source.y, y) - 35} ${x},${y}`,
        )
        path.setAttribute('class', 'globe-mesh-link')
        path.dataset.state = link.state
        path.addEventListener('click', () => options.current.enterGroup?.('link', link.id))
        groupLayer.appendChild(path)
        const label = document.createElementNS(svgNS, 'text')
        label.setAttribute('x', String((source.x + x) / 2))
        label.setAttribute('y', String((source.y + y) / 4 + (Math.min(source.y, y) - 35) / 2 - 8))
        label.setAttribute('text-anchor', 'middle')
        label.setAttribute('class', 'globe-mesh-caption')
        label.textContent = link.summary
        label.addEventListener('click', () => options.current.enterGroup?.('link', link.id))
        groupLayer.appendChild(label)
      }
      for (const { edge, midpoint } of lines) {
        const labelText = options.current.appearance?.edges[edge.id]?.label
        if (!labelText || midpoint.clone().normalize().dot(camera.position) < 1.04) continue
        project.copy(midpoint).project(camera)
        if (Math.abs(project.x) > 0.98 || Math.abs(project.y) > 0.98) continue
        const label = document.createElementNS(svgNS, 'text')
        label.setAttribute('x', String(gutter + (project.x * 0.5 + 0.5) * globeWidth))
        label.setAttribute('y', String((-project.y * 0.5 + 0.5) * height))
        label.setAttribute('text-anchor', 'middle')
        label.setAttribute('class', 'globe-mesh-caption')
        label.textContent = labelText
        label.addEventListener('click', () => options.current.select({ type: 'edge', id: edge.id }))
        groupLayer.appendChild(label)
      }
      // Balance the two gutters, preferring the previous side near the split
      // to avoid labels flickering left/right as adjacent markers move.
      const sideBias = (side?: 'left' | 'right') =>
        side === 'left' ? -10 : side === 'right' ? 10 : 0
      visibleNodes.sort(
        (a, b) =>
          a.x + sideBias(a.marker.side) - b.x - sideBias(b.marker.side) || a.id.localeCompare(b.id),
      )
      const split =
        visibleNodes.length === 1
          ? Number(visibleNodes[0].x < width / 2)
          : Math.ceil(visibleNodes.length / 2)
      const sides = { left: visibleNodes.slice(0, split), right: visibleNodes.slice(split) }
      for (const side of ['left', 'right'] as const) {
        const column = columns[side]
        const items = sides[side].sort((a, b) => a.y - b.y || a.id.localeCompare(b.id))
        column.viewport.hidden = items.length === 0
        column.viewport.style.top = `${top}px`
        column.viewport.style.height = `${available}px`
        column.viewport.style.width = `${Math.max(1, gutter - 24)}px`
        const contentHeight = Math.max(available, items.length * step)
        column.content.style.height = `${contentHeight}px`
        // Forward/backward sweeps keep vertical order and guarantee separation.
        const centers: number[] = []
        items.forEach((item, i) => {
          const target =
            THREE.MathUtils.clamp((item.y - top) / available, 0, 1) *
              (contentHeight - labelHeight) +
            labelHeight / 2
          centers.push(Math.max(target, i ? centers[i - 1] + step : labelHeight / 2))
        })
        for (let i = centers.length - 1; i >= 0; i--) {
          centers[i] = Math.min(
            centers[i],
            i + 1 < centers.length ? centers[i + 1] - step : contentHeight - labelHeight / 2,
          )
        }
        items.forEach(({ id, marker }, i) => {
          const on = options.current.appearance
            ? options.current.appearance.focusedNodes.includes(id)
            : options.current.selection?.type === 'node' && options.current.selection.id === id
          const wasSelected = marker.label.dataset.selected === 'true'
          marker.side = side
          if (marker.label.parentElement !== column.content)
            column.content.appendChild(marker.label)
          marker.label.style.transform = `translateY(${centers[i] - labelHeight / 2}px)`
          marker.label.dataset.selected = String(on)
          marker.leader.dataset.selected = String(on)
          if (on && !wasSelected) {
            if (centers[i] - labelHeight / 2 < column.viewport.scrollTop)
              column.viewport.scrollTop = centers[i] - labelHeight / 2
            else if (centers[i] + labelHeight / 2 > column.viewport.scrollTop + available)
              column.viewport.scrollTop = centers[i] + labelHeight / 2 - available
          }
        })
        const endX = side === 'left' ? 12 + column.viewport.clientWidth : width - gutter + 12
        const scrollTop = column.viewport.scrollTop
        items.forEach(({ marker, x, y }, i) => {
          const center = centers[i] - scrollTop
          if (center < labelHeight / 2 || center > available - labelHeight / 2) return
          const endY = top + center
          const elbowX = side === 'left' ? gutter + 8 : width - gutter - 8
          marker.leader.setAttribute('d', `M ${x} ${y} L ${elbowX} ${endY} L ${endX} ${endY}`)
          marker.leader.style.display = ''
        })
      }
    }
    const render = (time: number) => {
      frame = 0
      if (disposed || document.hidden) return
      const moving = (options.current.rotating && !reducedMotion.matches) || !!fly
      if (moving && time - last < 1000 / 30) {
        invalidate()
        return
      }
      const delta = Math.min((time - last) / 1000, 0.05)
      last = time
      if (fly) {
        const t = Math.min((time - fly.start) / 650, 1)
        const eased = t * t * (3 - 2 * t)
        // Spherical interpolation avoids cutting through the globe while flying.
        const direction = new THREE.Quaternion().setFromUnitVectors(
          fly.from.clone().normalize(),
          fly.to.clone().normalize(),
        )
        camera.position
          .copy(fly.from)
          .normalize()
          .applyQuaternion(new THREE.Quaternion().slerp(direction, eased))
          .multiplyScalar(THREE.MathUtils.lerp(fly.from.length(), fly.to.length(), eased))
        if (t === 1) fly = null
      }
      controls.autoRotate = options.current.rotating && !reducedMotion.matches && !fly
      controls.update(delta)
      renderer.render(scene, camera)
      updateLabels()
      if (moving) invalidate()
    }
    function invalidate() {
      if (!frame && !disposed && !document.hidden) frame = requestAnimationFrame(render)
    }
    const refresh = () => {
      const selected = options.current.selection
      const appearance = options.current.appearance
      const currentNodes = new Map(options.current.nodes.map((node) => [node.id, node]))
      for (const [id, marker] of markers) {
        const node = currentNodes.get(id)
        if (node) {
          marker.label.textContent = node.name
          marker.label.title = `${node.name} · ${node.city} · ${node.ip}`
        }
        const on = appearance
          ? appearance.focusedNodes.includes(id)
          : selected?.type === 'node' && selected.id === id
        const color = on ? amber : appearance?.nodes[id]?.color || teal
        marker.mesh.material.color.set(color)
        marker.halo.material.color.set(color)
        if (appearance?.nodes[id]) marker.label.title = appearance.nodes[id].title
        marker.mesh.scale.setScalar(on ? 1.4 : 1)
        marker.halo.scale.setScalar(on ? 1.25 : 1)
      }
      for (const { edge, mesh } of lines) {
        const on = appearance
          ? appearance.focusedEdges.includes(edge.id)
          : selected?.type === 'edge'
            ? selected.id === edge.id
            : selected?.type === 'node' && (edge.a === selected.id || edge.b === selected.id)
        const style = appearance?.edges[edge.id]
        mesh.material.color.set(on ? amber : style?.color || teal)
        mesh.material.dashSize = style?.dashed ? 0.018 : 100
        mesh.material.gapSize = style?.dashed ? 0.012 : 0
        mesh.material.opacity = appearance?.dimmed || selected ? (on ? 0.95 : 0.17) : 0.7
      }
      invalidate()
    }
    const move = (to: THREE.Vector3) => {
      if (reducedMotion.matches) {
        camera.position.copy(to)
        controls.update()
      } else fly = { from: camera.position.clone(), to, start: performance.now() }
      invalidate()
    }
    api.current = {
      refresh,
      command: ({ kind, id }) => {
        if (kind === 'reset') move(home.clone())
        else if (kind === 'focus' && id && markers.has(id))
          move(markers.get(id)!.point.clone().normalize().multiplyScalar(3.3))
        else if (kind === 'in' || kind === 'out')
          move(
            camera.position
              .clone()
              .normalize()
              .multiplyScalar(
                THREE.MathUtils.clamp(
                  camera.position.length() * (kind === 'in' ? 0.8 : 1.25),
                  controls.minDistance,
                  controls.maxDistance,
                ),
              ),
          )
      },
    }
    const resize = new ResizeObserver(([entry]) => {
      if (!entry.contentRect.width || !entry.contentRect.height) return
      const previousAspect = camera.aspect
      width = entry.contentRect.width
      height = entry.contentRect.height
      const gutter = gutterFor(width)
      camera.aspect = (width - 2 * gutter) / height
      camera.updateProjectionMatrix()
      // Fit the whole sphere even in a narrow, tall panel. Preserve the user's
      // relative zoom when the panel dimensions change.
      const fit = (aspect: number) =>
        1.35 / Math.sin(Math.atan(Math.tan(THREE.MathUtils.degToRad(21)) * Math.min(1, aspect)))
      const fitted = fit(camera.aspect)
      home.setLength(fitted)
      camera.position.setLength(
        THREE.MathUtils.clamp(
          (camera.position.length() * fitted) / fit(previousAspect),
          controls.minDistance,
          controls.maxDistance,
        ),
      )
      fly = null
      controls.update()
      renderer.setSize(width, height)
      renderer.setViewport(gutter, 0, width - 2 * gutter, height)
      leaders.setAttribute('viewBox', `0 0 ${width} ${height}`)
      invalidate()
    })
    resize.observe(element)
    const raycaster = new THREE.Raycaster()
    raycaster.params.Line.threshold = 0.012
    const down = new THREE.Vector2()
    let pressed = false
    let source: string | undefined
    const pick = (e: PointerEvent) => {
      const bounds = renderer.domElement.getBoundingClientRect()
      const gutter = gutterFor(width)
      const x = e.clientX - bounds.left - gutter
      const globeWidth = width - gutter * 2
      if (x < 0 || x > globeWidth || e.clientY < bounds.top || e.clientY > bounds.bottom)
        return null
      raycaster.setFromCamera(
        new THREE.Vector2((x / globeWidth) * 2 - 1, (-(e.clientY - bounds.top) / height) * 2 + 1),
        camera,
      )
      // Include the opaque earth so nodes/edges on the back cannot be selected.
      const hit = raycaster.intersectObjects([earth, ...picks], false)[0]
      return (hit?.object.userData.selection || null) as Selection
    }
    const pointerDown = (e: PointerEvent) => {
      if (e.button !== 0) return
      pressed = true
      down.set(e.clientX, e.clientY)
      const hit = pick(e)
      source = options.current.connect && hit?.type === 'node' ? hit.id : undefined
      if (source) controls.enableRotate = false
    }
    const pointerUp = (e: PointerEvent) => {
      if (!pressed) return
      pressed = false
      controls.enableRotate = true
      const hit = pick(e)
      if (down.distanceTo(new THREE.Vector2(e.clientX, e.clientY)) > 5) {
        if (source && hit?.type === 'node' && hit.id !== source)
          options.current.connect?.(source, hit.id)
        source = undefined
        return
      }
      source = undefined
      options.current.select(hit, e.ctrlKey || e.metaKey)
    }
    const cancelFly = () => {
      fly = null
    }
    const cancelPointer = () => {
      pressed = false
      source = undefined
      controls.enableRotate = true
    }
    controls.addEventListener('change', invalidate)
    controls.addEventListener('start', cancelFly)
    renderer.domElement.addEventListener('pointerdown', pointerDown, true)
    window.addEventListener('pointerup', pointerUp)
    window.addEventListener('pointercancel', cancelPointer)
    document.addEventListener('visibilitychange', invalidate)
    reducedMotion.addEventListener('change', invalidate)
    columns.left.viewport.addEventListener('scroll', invalidate)
    columns.right.viewport.addEventListener('scroll', invalidate)
    refresh()
    return () => {
      disposed = true
      cameraPosition.current = camera.position.clone()
      cameraAspect.current = camera.aspect
      cancelAnimationFrame(frame)
      resize.disconnect()
      controls.dispose()
      api.current = null
      document.removeEventListener('visibilitychange', invalidate)
      reducedMotion.removeEventListener('change', invalidate)
      renderer.domElement.removeEventListener('pointerdown', pointerDown, true)
      window.removeEventListener('pointerup', pointerUp)
      window.removeEventListener('pointercancel', cancelPointer)
      for (const { label } of markers.values()) label.remove()
      leaders.remove()
      groupLayer.remove()
      columns.left.viewport.remove()
      columns.right.viewport.remove()
      scene.traverse((object) => {
        if (object instanceof THREE.Mesh || object instanceof THREE.Line) {
          object.geometry.dispose()
          const materials = Array.isArray(object.material) ? object.material : [object.material]
          materials.forEach((material) => material.dispose())
        }
      })
      texture.dispose()
      renderer.dispose()
      renderer.forceContextLoss()
      renderer.domElement.remove()
    }
  }, [geometryKey])
  useEffect(() => {
    api.current?.refresh()
  }, [nodes, selection, rotating, labels, appearance, groups, groupLinks])
  useEffect(() => {
    api.current?.command(command)
  }, [command])
  return (
    <div className="globe-renderer" ref={host}>
      {error && (
        <div className="globe-error" role="alert">
          {error}
        </div>
      )}
    </div>
  )
}
