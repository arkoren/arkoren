import {
  AmbientLight,
  Box3,
  BufferGeometry,
  Color,
  DirectionalLight,
  EdgesGeometry,
  ExtrudeGeometry,
  Float32BufferAttribute,
  Group,
  LineBasicMaterial,
  LineSegments,
  Mesh,
  MeshStandardMaterial,
  PCFShadowMap,
  PerspectiveCamera,
  Raycaster,
  Scene,
  Shape,
  SphereGeometry,
  Vector2,
  Vector3,
  WebGLRenderer,
} from "three";
import { createInstrument } from "../lib/figure/instrument";

/** Builds an actual depth-tested WebGL rack, keeping an SVG fallback until ready. */
export function createWebGL(root: HTMLElement) {
  const stage = root.querySelector<HTMLElement>("[data-bench-stage]");
  if (stage === null) return null;
  const canvas = document.createElement("canvas");
  canvas.setAttribute("aria-hidden", "true");
  let renderer: WebGLRenderer;
  try {
    renderer = new WebGLRenderer({ canvas, alpha: true, antialias: true });
  } catch {
    root.dataset.renderer = "svg";
    return null;
  }
  renderer.setPixelRatio(Math.min(devicePixelRatio, 1.5));
  renderer.shadowMap.enabled = true;
  renderer.shadowMap.type = PCFShadowMap;
  const scene = new Scene();
  const camera = new PerspectiveCamera(35, 1, 1, 4000);
  camera.up.set(0, 0, 1);
  scene.add(new AmbientLight(0xffffff, 1.1));
  const light = new DirectionalLight(0xffffff, 2);
  light.position.set(-280, 350, 800);
  light.target.position.set(0, 0, 260);
  light.castShadow = true;
  light.shadow.mapSize.set(1024, 1024);
  Object.assign(light.shadow.camera, {
    left: -360,
    right: 360,
    top: 360,
    bottom: -360,
    near: 1,
    far: 1400,
  });
  light.shadow.normalBias = 0.5;
  scene.add(light, light.target);
  const groups = new Map<string, Group>();
  const materials = new Map<string, MeshStandardMaterial>();
  const edges: LineBasicMaterial[] = [];
  const targets: Mesh[] = [];

  /** Resolve light-dark() and seasonal tokens through the browser's CSS engine. */
  function color(token: string) {
    const probe = document.createElement("span");
    probe.style.color = `var(${token})`;
    root.append(probe);
    const value = new Color(getComputedStyle(probe).color);
    probe.remove();
    return value;
  }
  const palette = {
    body: color("--color-figure-metal"),
    edge: color("--color-figure-edge"),
    accent: color("--color-accent-text"),
  };
  /** Builds static meshes once; orbiting only changes the camera. */
  function build() {
    for (const solid of createInstrument()) {
      let group = groups.get(solid.piece);
      if (group === undefined) {
        group = new Group();
        groups.set(solid.piece, group);
        scene.add(group);
      }
      let material = materials.get(solid.piece);
      if (material === undefined) {
        material = new MeshStandardMaterial({
          color: palette.body,
          roughness: 0.62,
          metalness: 0.25,
          polygonOffset: true,
          polygonOffsetFactor: 1,
          polygonOffsetUnits: 1,
        });
        materials.set(solid.piece, material);
      }
      const plan = solid.plan ?? {
        x: (solid.x ?? 0) - (solid.r ?? 1),
        y: (solid.y ?? 0) - (solid.r ?? 1),
        w: (solid.r ?? 1) * 2,
        d: (solid.r ?? 1) * 2,
        r: solid.r ?? 1,
      };
      const shape = new Shape();
      const r = Math.min(plan.r, plan.w / 2, plan.d / 2),
        x = plan.x,
        y = plan.y,
        w = plan.w,
        d = plan.d;
      shape.moveTo(x + r, y);
      shape.lineTo(x + w - r, y);
      shape.quadraticCurveTo(x + w, y, x + w, y + r);
      shape.lineTo(x + w, y + d - r);
      shape.quadraticCurveTo(x + w, y + d, x + w - r, y + d);
      shape.lineTo(x + r, y + d);
      shape.quadraticCurveTo(x, y + d, x, y + d - r);
      shape.lineTo(x, y + r);
      shape.quadraticCurveTo(x, y, x + r, y);
      if (solid.profile) {
        shape.curves.length = 0;
        solid.profile.forEach(([x, z], index) => {
          if (index === 0) shape.moveTo(x, z);
          else shape.lineTo(x, z);
        });
        shape.closePath();
        for (const outline of solid.holes ?? []) {
          const hole = new Shape();
          outline.forEach(([x, z], index) => {
            if (index === 0) hole.moveTo(x, z);
            else hole.lineTo(x, z);
          });
          hole.closePath();
          shape.holes.push(hole);
        }
      }
      const geometry = new ExtrudeGeometry(shape, {
        depth: solid.profile ? solid.depth : solid.h,
        bevelEnabled: false,
        curveSegments: 6,
        steps: 1,
      });
      if (solid.profile) {
        geometry.rotateX(Math.PI / 2);
        geometry.translate(0, (solid.y ?? 0) + (solid.depth ?? 1), 0);
      } else geometry.translate(0, 0, solid.z);
      const mesh = new Mesh(geometry, material);
      mesh.userData.piece = solid.piece;
      mesh.castShadow = true;
      mesh.receiveShadow = true;
      group.add(mesh);
      targets.push(mesh);
      const ink = new LineBasicMaterial({ color: palette.edge, transparent: true, opacity: 0.42 });
      edges.push(ink);
      group.add(new LineSegments(new EdgesGeometry(geometry, 30), ink));
      const vertices: number[] = [];
      for (const line of solid.lines)
        for (let i = 1; i < line.points.length; i++) {
          if (line.starts[i]) continue;
          const from = line.points[i - 1],
            to = line.points[i];
          vertices.push(from[0], from[1], from[2] + 0.12, to[0], to[1], to[2] + 0.12);
        }
      if (vertices.length > 0) {
        const detail = new BufferGeometry();
        detail.setAttribute("position", new Float32BufferAttribute(vertices, 3));
        group.add(new LineSegments(detail, ink));
      }
      for (const dot of solid.dots) {
        const screw = new Mesh(new SphereGeometry(0.65, 6, 4), material);
        screw.position.fromArray(dot.at);
        group.add(screw);
      }
    }
  }
  build();
  /** A real pointer on the application selector shares its original world mount. */
  const pointerGeometry = new BufferGeometry();
  pointerGeometry.setAttribute("position", new Float32BufferAttribute(new Float32Array(6), 3));
  const pointer = new LineSegments(
    pointerGeometry,
    new LineBasicMaterial({ color: palette.accent }),
  );
  scene.add(pointer);
  let active = "go";
  const current = new Map([...materials].map(([id]) => [id, 0]));
  const ray = new Raycaster();
  const mouse = new Vector2();
  const observer = new MutationObserver(() => {
    palette.body.copy(color("--color-figure-metal"));
    palette.accent.copy(color("--color-accent-text"));
    root.dispatchEvent(new Event("bench-redraw"));
  });
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-season"],
  });
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    palette.body.copy(color("--color-figure-metal"));
    palette.edge.copy(color("--color-figure-edge"));
    palette.accent.copy(color("--color-accent-text"));
    for (const ink of edges) ink.color.copy(palette.edge);
    root.dispatchEvent(new Event("bench-redraw"));
  });
  stage.append(canvas);
  root.dataset.renderer = "webgl";
  canvas.addEventListener("webglcontextlost", (event) => {
    event.preventDefault();
    root.dataset.renderer = "svg";
  });
  canvas.addEventListener("webglcontextrestored", () => {
    root.dataset.renderer = "webgl";
    root.dispatchEvent(new Event("bench-redraw"));
  });
  return {
    /** Refit the instrument to the current viewport without cropping its top. */
    draw(
      view: { azimuth: number; elevation: number; zoom: number; x: number; y: number; z: number },
      needle: number,
      dt: number,
      calm: boolean,
    ) {
      const width = stage.clientWidth,
        height = stage.clientHeight;
      if (
        canvas.width !== Math.round(width * renderer.getPixelRatio()) ||
        canvas.height !== Math.round(height * renderer.getPixelRatio())
      )
        renderer.setSize(width, height, false);
      camera.aspect = width / height;
      camera.updateProjectionMatrix();
      const distance =
        (Math.max(
          190 / Math.tan((35 * Math.PI) / 360),
          205 / (camera.aspect * Math.tan((35 * Math.PI) / 360)),
        ) *
          1.08) /
        view.zoom;
      const a = (view.azimuth * Math.PI) / 180,
        e = (view.elevation * Math.PI) / 180;
      const aim = new Vector3(view.x, view.y, view.z);
      camera.position.set(
        aim.x + distance * Math.cos(a) * Math.cos(e),
        aim.y + distance * Math.sin(a) * Math.cos(e),
        aim.z + distance * Math.sin(e),
      );
      camera.lookAt(aim);
      let changing = false;
      for (const [id, material] of materials) {
        const value = current.get(id) ?? 0,
          target = id === active ? 1 : 0;
        const next = calm ? target : value + (target - value) * (1 - Math.exp(-dt / 0.18));
        current.set(id, next);
        material.color.copy(palette.body).lerp(palette.accent, next * 0.25);
        material.emissive.copy(palette.accent);
        material.emissiveIntensity = next * 0.12;
        changing ||= Math.abs(next - target) > 0.005;
      }
      const angle = (needle * Math.PI) / 180;
      const positions = pointerGeometry.getAttribute("position");
      const start: [number, number, number] = [0, 37, 287];
      const end: [number, number, number] = [10 * Math.sin(angle), 37, 287 + 10 * Math.cos(angle)];
      positions.setXYZ(0, ...start);
      positions.setXYZ(1, ...end);
      positions.needsUpdate = true;
      renderer.render(scene, camera);
      return changing;
    },
    select(id: string) {
      active = id;
    },
    center(id: string) {
      const group = groups.get(id);
      return group
        ? new Box3().setFromObject(group).getCenter(new Vector3())
        : new Vector3(0, 0, 270);
    },
    pick(event: PointerEvent) {
      const box = canvas.getBoundingClientRect();
      mouse.set(
        ((event.clientX - box.left) / box.width) * 2 - 1,
        (-(event.clientY - box.top) / box.height) * 2 + 1,
      );
      ray.setFromCamera(mouse, camera);
      return ray.intersectObjects(targets, false)[0]?.object.userData.piece as string | undefined;
    },
    dispose() {
      observer.disconnect();
      scene.traverse((object) => {
        if (object instanceof Mesh || object instanceof LineSegments) {
          object.geometry.dispose();
          const values = Array.isArray(object.material) ? object.material : [object.material];
          for (const material of values) material.dispose();
        }
      });
      renderer.dispose();
      canvas.remove();
    },
  };
}
