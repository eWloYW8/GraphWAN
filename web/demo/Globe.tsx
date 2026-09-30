import { useEffect, useRef, useState } from 'react'
import * as THREE from 'three'
import { OrbitControls } from 'three/addons/controls/OrbitControls.js'
import countries from './assets/countries.json'
import type { GlobeNode, GlobeEdge, Selection } from './data'

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
  edges,
  selection,
  select,
  rotating,
  labels,
  command,
}: {
  nodes: GlobeNode[]
  edges: GlobeEdge[]
  selection: Selection
  select: (selection: Selection) => void
  rotating: boolean
  labels: boolean
  command: ViewCommand
}) {
  const host = useRef<HTMLDivElement>(null)
  const [error, setError] = useState('')
  const options = useRef({ selection, select, rotating, labels })
  options.current = { selection, select, rotating, labels }
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
    const scene = new THREE.Scene()
    const camera = new THREE.PerspectiveCamera(42, 1, 0.1, 100)
    const home = position(24, 48).multiplyScalar(3.75)
    camera.position.copy(home)
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
    const markers = new Map<
      string,
      {
        mesh: THREE.Mesh<THREE.SphereGeometry, THREE.MeshBasicMaterial>
        halo: THREE.Mesh<THREE.RingGeometry, THREE.MeshBasicMaterial>
        point: THREE.Vector3
        label: HTMLButtonElement
      }
    >()
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
        label.onclick = () => options.current.select({ type: 'node', id: node.id })
        element.appendChild(label)
        markers.set(node.id, { mesh, halo, point, label })
      })
    }
    const lines = edges.flatMap((edge) => {
      const a = markers.get(edge.a),
        b = markers.get(edge.b)
      if (!a || !b) return []
      const points = arc(a.point, b.point)
      const curve = new THREE.CatmullRomCurve3(points)
      const mesh = new THREE.Mesh(
        new THREE.TubeGeometry(curve, 96, 0.0016, 5, false),
        new THREE.MeshBasicMaterial({ color: teal, transparent: true, opacity: 0.72 }),
      )
      mesh.userData.selection = { type: 'edge', id: edge.id }
      scene.add(mesh)
      picks.push(mesh)
      return [{ edge, mesh }]
    })

    let width = 1,
      height = 1,
      frame = 0,
      disposed = false,
      last = 0
    let fly: { from: THREE.Vector3; to: THREE.Vector3; start: number } | null = null
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
    const project = new THREE.Vector3()
    const updateLabels = () => {
      const boxes: { x: number; y: number; w: number; h: number }[] = []
      const ordered = [...markers.entries()].sort(
        ([a], [b]) =>
          Number(options.current.selection?.id === b) - Number(options.current.selection?.id === a),
      )
      for (const [id, marker] of ordered) {
        const { label, point } = marker
        project.copy(point).project(camera)
        const visible =
          options.current.labels &&
          point.dot(camera.position) > 1.04 &&
          Math.abs(project.x) < 0.98 &&
          Math.abs(project.y) < 0.96
        label.hidden = !visible
        if (!visible) continue
        const x = (project.x * 0.5 + 0.5) * width,
          y = (-project.y * 0.5 + 0.5) * height
        const w = label.offsetWidth || 110,
          h = 26
        const shifts = [
          [14, -14],
          [14, 15],
          [-w - 14, -14],
          [-w - 14, 15],
          [14, -44],
          [-w / 2, -58],
        ]
        const spot = shifts
          .map(([dx, dy]) => ({ x: x + dx, y: y + dy, w, h }))
          .find(
            (r) =>
              r.x > 8 &&
              r.x + w < width - 8 &&
              r.y > 8 &&
              r.y + h < height - 8 &&
              !boxes.some(
                (b) =>
                  r.x < b.x + b.w + 5 &&
                  r.x + w + 5 > b.x &&
                  r.y < b.y + b.h + 3 &&
                  r.y + h + 3 > b.y,
              ),
          )
        label.hidden = !spot
        if (spot) {
          boxes.push(spot)
          label.style.transform = `translate(${spot.x}px, ${spot.y}px)`
          label.dataset.selected = String(
            options.current.selection?.type === 'node' && options.current.selection.id === id,
          )
        }
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
      for (const [id, marker] of markers) {
        const on = selected?.type === 'node' && selected.id === id
        marker.mesh.material.color.set(on ? amber : teal)
        marker.halo.material.color.set(on ? amber : teal)
        marker.mesh.scale.setScalar(on ? 1.4 : 1)
        marker.halo.scale.setScalar(on ? 1.25 : 1)
      }
      for (const { edge, mesh } of lines) {
        const on =
          selected?.type === 'edge'
            ? selected.id === edge.id
            : selected?.type === 'node' && (edge.a === selected.id || edge.b === selected.id)
        mesh.material.color.set(on ? amber : teal)
        mesh.material.opacity = selected ? (on ? 0.95 : 0.17) : 0.7
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
      const previousAspect = width / height
      width = entry.contentRect.width
      height = entry.contentRect.height
      if (!width || !height) return
      camera.aspect = width / height
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
      invalidate()
    })
    resize.observe(element)
    const raycaster = new THREE.Raycaster()
    const down = new THREE.Vector2()
    const pointerDown = (e: PointerEvent) => down.set(e.clientX, e.clientY)
    const pointerUp = (e: PointerEvent) => {
      if (down.distanceTo(new THREE.Vector2(e.clientX, e.clientY)) > 5) return
      const bounds = renderer.domElement.getBoundingClientRect()
      raycaster.setFromCamera(
        new THREE.Vector2(
          ((e.clientX - bounds.left) / width) * 2 - 1,
          (-(e.clientY - bounds.top) / height) * 2 + 1,
        ),
        camera,
      )
      // Include the opaque earth so nodes/edges on the back cannot be selected.
      const hit = raycaster.intersectObjects([earth, ...picks], false)[0]
      options.current.select(hit?.object.userData.selection || null)
    }
    const cancelFly = () => {
      fly = null
    }
    controls.addEventListener('change', invalidate)
    controls.addEventListener('start', cancelFly)
    renderer.domElement.addEventListener('pointerdown', pointerDown)
    renderer.domElement.addEventListener('pointerup', pointerUp)
    document.addEventListener('visibilitychange', invalidate)
    reducedMotion.addEventListener('change', invalidate)
    refresh()
    return () => {
      disposed = true
      cancelAnimationFrame(frame)
      resize.disconnect()
      controls.dispose()
      api.current = null
      document.removeEventListener('visibilitychange', invalidate)
      reducedMotion.removeEventListener('change', invalidate)
      renderer.domElement.removeEventListener('pointerdown', pointerDown)
      renderer.domElement.removeEventListener('pointerup', pointerUp)
      for (const { label } of markers.values()) label.remove()
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
  }, [nodes, edges])
  useEffect(() => {
    api.current?.refresh()
  }, [selection, rotating, labels])
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
