import { useEffect, useRef, useState } from 'react'
import * as THREE from 'three'
import { OrbitControls } from 'three/addons/controls/OrbitControls.js'

interface ModelMesh {
  name?: string
  color?: [number, number, number]
  positions: number[]
  normals?: number[]
  indices: number[]
}

interface ModelPayload { format: string; meshes: ModelMesh[]; triangles: number }

export function ModelViewer({ path }: { path: string }) {
  const mountRef = useRef<HTMLDivElement | null>(null)
  const [status, setStatus] = useState<string>('Loading model…')
  const [info, setInfo] = useState<string>('')

  useEffect(() => {
    const mount = mountRef.current
    if (!mount) return
    let disposed = false
    let raf = 0
    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true })
    renderer.setPixelRatio(window.devicePixelRatio)
    mount.appendChild(renderer.domElement)

    const scene = new THREE.Scene()
    const camera = new THREE.PerspectiveCamera(45, 1, 0.01, 10000)
    scene.add(new THREE.HemisphereLight(0xffffff, 0x64748b, 1.6))
    const key = new THREE.DirectionalLight(0xffffff, 1.4)
    key.position.set(1, 2, 1.5)
    scene.add(key)

    const controls = new OrbitControls(camera, renderer.domElement)
    controls.enableDamping = true

    const size = () => {
      const w = mount.clientWidth || 600
      const h = mount.clientHeight || 420
      renderer.setSize(w, h)
      camera.aspect = w / h
      camera.updateProjectionMatrix()
    }
    size()
    const ro = new ResizeObserver(size)
    ro.observe(mount)

    fetch(`/api/kb/model?path=${encodeURIComponent(path)}`)
      .then(async r => {
        if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error ?? `${r.status}`)
        return r.json() as Promise<ModelPayload>
      })
      .then(payload => {
        if (disposed) return
        const group = new THREE.Group()
        for (const m of payload.meshes) {
          const geo = new THREE.BufferGeometry()
          geo.setAttribute('position', new THREE.Float32BufferAttribute(m.positions, 3))
          if (m.normals?.length) geo.setAttribute('normal', new THREE.Float32BufferAttribute(m.normals, 3))
          geo.setIndex(m.indices)
          if (!m.normals?.length) geo.computeVertexNormals()
          const color = m.color
            ? new THREE.Color(m.color[0], m.color[1], m.color[2])
            : new THREE.Color('#7fa3c0')
          const mat = new THREE.MeshStandardMaterial({ color, metalness: 0.15, roughness: 0.6 })
          group.add(new THREE.Mesh(geo, mat))
        }
        // Center the part and back the camera off along the box diagonal.
        const box = new THREE.Box3().setFromObject(group)
        const center = box.getCenter(new THREE.Vector3())
        const radius = box.getSize(new THREE.Vector3()).length() / 2 || 1
        group.position.sub(center)
        scene.add(group)

        const grid = new THREE.GridHelper(radius * 4, 20, 0x94a3b8, 0xcbd5e1)
        ;(grid.material as THREE.Material).transparent = true
        ;(grid.material as THREE.Material).opacity = 0.35
        grid.position.y = box.min.y - center.y
        scene.add(grid)

        camera.position.set(radius * 1.6, radius * 1.2, radius * 1.6)
        camera.near = radius / 100
        camera.far = radius * 40
        camera.updateProjectionMatrix()
        controls.update()

        setStatus('')
        setInfo(`${payload.format.toUpperCase()}, ${payload.triangles.toLocaleString()} triangles, ${payload.meshes.length} ${payload.meshes.length === 1 ? 'body' : 'bodies'}. Drag to rotate, scroll to zoom, right-drag to pan.`)
      })
      .catch(e => { if (!disposed) setStatus(`Could not load model: ${String((e as Error).message ?? e)}`) })

    const tick = () => {
      controls.update()
      renderer.render(scene, camera)
      raf = requestAnimationFrame(tick)
    }
    tick()

    return () => {
      disposed = true
      cancelAnimationFrame(raf)
      ro.disconnect()
      controls.dispose()
      renderer.dispose()
      scene.traverse(o => {
        const mesh = o as THREE.Mesh
        if (mesh.geometry) mesh.geometry.dispose()
        const mat = mesh.material as THREE.Material | THREE.Material[] | undefined
        if (Array.isArray(mat)) mat.forEach(m => m.dispose())
        else mat?.dispose()
      })
      mount.removeChild(renderer.domElement)
    }
  }, [path])

  return (
    <div className="model-viewer">
      <div ref={mountRef} className="model-canvas" />
      {status && <p className="muted pad">{status}</p>}
      {info && <p className="model-info">{info}</p>}
    </div>
  )
}
