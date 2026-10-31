import original from "./scene.json";
import type { Plan, Point3 } from "./geometry";

/** A solid and its face details share world coordinates for SVG and WebGL. */
export interface Solid {
  /** An x/z face profile is extruded along y for logo-shaped housings. */
  profile?: [number, number][];
  holes?: [number, number][][];
  depth?: number;
  piece: string;
  plan?: Plan;
  x?: number;
  y?: number;
  r?: number;
  z: number;
  h: number;
  kind: string;
  tone: string;
  lines: { points: Point3[]; starts: boolean[]; tone: string }[];
  dots: { at: Point3; r: number }[];
}

/** Shelves are a physical arrangement, not a required dependency hierarchy. */
const mounts: Record<string, { center: number[]; scale: number[]; at: number[] }> = {
  foundation: { center: [110, 50], scale: [1.5, 1.2, 1.2], at: [0, 0, 20] },
  drivers: { center: [112, -52], scale: [1.35, 1.25, 1], at: [0, 0, 104] },
  services: { center: [-16, 1], scale: [1.3, 1, 1], at: [0, 17, 194] },
  events: { center: [-20, -75], scale: [1.3, 0.6, 1], at: [0, -55, 194] },
  http: { center: [-137, 16], scale: [2.4, 0.85, 1.4], at: [0, 0, 284] },
  application: { center: [-36, 63], scale: [1.5, 1.5, 1.1], at: [0, 0, 374] },
  tools: { center: [-131, -78], scale: [2.2, 2.8, 1.3], at: [0, 0, 464] },
};

/** Transforms one module into its shelf without changing its internal contacts. */
export function rackPoint(piece: string, point: readonly number[]): Point3 {
  const mount = mounts[piece];
  return [
    (point[0] - mount.center[0]) * mount.scale[0] + mount.at[0],
    (point[1] - mount.center[1]) * mount.scale[1] + mount.at[1],
    (point[2] - 2) * mount.scale[2] + mount.at[2],
  ];
}

/** Constructs the supported shelves, rails, feet and plug-in instruments. */
export function createRack(): Solid[] {
  const solids: Solid[] = original
    .filter((shape) => shape.piece !== "stand")
    .map((shape) => {
      const mount = mounts[shape.piece];
      const position = rackPoint(shape.piece, [
        shape.plan?.x ?? shape.x ?? 0,
        shape.plan?.y ?? shape.y ?? 0,
        shape.z,
      ]);
      return {
        piece: shape.piece,
        kind: shape.kind,
        tone: shape.tone,
        plan: shape.plan
          ? {
              x: position[0],
              y: position[1],
              w: shape.plan.w * mount.scale[0],
              d: shape.plan.d * mount.scale[1],
              r: shape.plan.r,
            }
          : undefined,
        x: position[0],
        y: position[1],
        r: shape.r ? shape.r * Math.min(mount.scale[0], mount.scale[1]) : undefined,
        z: position[2],
        h: shape.h * mount.scale[2],
        lines: shape.lines.map((line) => ({
          ...line,
          points: line.points.map((point) => rackPoint(shape.piece, point)),
        })),
        dots: shape.dots.map((dot) => ({ ...dot, at: rackPoint(shape.piece, dot.at) })),
      };
    });
  /** Frame geometry stays separate from selectable application modules. */
  function plate(plan: Plan, z: number, h: number) {
    solids.push({ piece: "stand", plan, z, h, kind: "slab", tone: "lo", lines: [], dots: [] });
  }
  plate({ x: -118, y: -91, w: 236, d: 182, r: 9 }, -6, 12);
  for (const x of [-106, 98])
    for (const y of [-78, 70]) {
      plate({ x, y, w: 8, d: 8, r: 1 }, 6, 540);
      plate({ x: x - 3, y: y - 3, w: 14, d: 14, r: 3 }, -14, 8);
    }
  for (const z of [16, 100, 190, 280, 370, 460]) {
    plate({ x: -98, y: -70, w: 196, d: 140, r: 3 }, z, 4);
    for (const x of [-106, 98]) plate({ x, y: -70, w: 8, d: 140, r: 1 }, z - 4, 4);
    for (const y of [-78, 70]) plate({ x: -98, y, w: 196, d: 8, r: 1 }, z - 4, 4);
  }
  plate({ x: -110, y: -82, w: 220, d: 164, r: 5 }, 546, 6);
  return solids;
}
