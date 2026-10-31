import { createInstrument } from "./instrument";
import {
  boxCorners,
  cylinder,
  fitProjection,
  slabOf,
  depthOf,
  iso,
  pathOf,
  type Point3,
} from "./geometry";

/** The same logo-shaped solids remain available without a graphics context. */
export function rackFallback() {
  const solids = createInstrument();
  const points: Point3[] = solids.flatMap((s) =>
    s.profile
      ? s.profile.flatMap(
          ([x, z]) =>
            [
              [x, s.y ?? 0, z],
              [x, (s.y ?? 0) + (s.depth ?? 1), z],
            ] as Point3[],
        )
      : boxCorners(s.plan ?? { x: 0, y: 0, w: 1, d: 1, r: 0 }, s.z, s.z + s.h),
  );
  const projection = fitProjection(points, 620, 620, { pad: 24, azimuth: 70, elevation: 12 });
  const center = (s: (typeof solids)[number]): Point3 =>
    s.profile
      ? [
          s.profile.reduce((sum, p) => sum + p[0], 0) / s.profile.length,
          (s.y ?? 0) + (s.depth ?? 0) / 2,
          s.z + s.h / 2,
        ]
      : [
          s.plan ? s.plan.x + s.plan.w / 2 : (s.x ?? 0),
          s.plan ? s.plan.y + s.plan.d / 2 : (s.y ?? 0),
          s.z + s.h / 2,
        ];
  return solids
    .sort((a, b) => depthOf(center(a), projection) - depthOf(center(b), projection))
    .map((s) => {
      if (s.profile) {
        const polygon = (y: number, outline = s.profile ?? []) =>
          pathOf(
            outline.map(([x, z]) => iso([x, y, z], projection)),
            true,
          );
        const back = s.y ?? 0,
          front = back + (s.depth ?? 1);
        const sides = s.profile
          .map(([x, z], index) => {
            const next = s.profile?.[(index + 1) % (s.profile?.length ?? 1)] ?? [x, z];
            return `<path class="iso-shade" data-shade="${index % 4}" d="${pathOf(
              [
                [x, back, z],
                [next[0], back, next[1]],
                [next[0], front, next[1]],
                [x, front, z],
              ].map((p) => iso(p as Point3, projection)),
              true,
            )}"/>`;
          })
          .join("");
        const face = polygon(front) + (s.holes ?? []).map((h) => polygon(front, h)).join("");
        return `<g data-assembly="${s.piece}"><path class="iso-fill" d="${polygon(back)}"/>${sides}<path class="iso-top" fill-rule="evenodd" d="${face}"/><path class="iso-line" d="${face}"/></g>`;
      }
      const p = s.plan
        ? slabOf(s.plan, s.z, s.h, projection, 6, 0.5)
        : cylinder(s.x ?? 0, s.y ?? 0, s.r ?? 1, s.z, s.h, projection, 24, 0.3);
      return `<g data-assembly="${s.piece}"><path class="iso-fill" d="${p.fill}"/>${p.shades.map((d, i) => `<path class="iso-shade" data-shade="${i}" d="${d}"/>`).join("")}<path class="iso-top" d="${p.top}"/><path class="iso-line" d="${p.outline}"/></g>`;
    })
    .join("");
}
