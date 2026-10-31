import type { Solid } from "./rack";
import type { Plan } from "./geometry";

/** Front profiles follow the actual mark in public/favicon.svg, scaled by four. */
const bands: { piece: string; outline: [number, number][] }[] = [
  {
    piece: "go",
    outline: [
      [-184, 0],
      [-84, 0],
      [-48, 88],
      [-144, 88],
    ],
  },
  {
    piece: "foundation",
    outline: [
      [84, 0],
      [184, 0],
      [144, 88],
      [48, 88],
    ],
  },
  {
    piece: "core",
    outline: [
      [-128, 128],
      [128, 128],
      [92, 208],
      [-92, 208],
    ],
  },
  {
    piece: "app",
    outline: [
      [-76, 248],
      [76, 248],
      [40, 328],
      [-40, 328],
    ],
  },
];

/** Produces a supported object, with one coordinate system for WebGL and SVG. */
export function createInstrument(): Solid[] {
  const out: Solid[] = [];
  const depth = 60;
  function plate(piece: string, plan: Plan, z: number, h: number) {
    out.push({ piece, plan, z, h, kind: "slab", tone: "mid", lines: [], dots: [] });
  }
  function box(piece: string, x: number, y: number, z: number, w: number, d: number, h: number) {
    plate(piece, { x, y, w, d, r: 1 }, z, h);
  }
  function profile(
    piece: string,
    outline: [number, number][],
    y: number,
    d: number,
    holes: [number, number][][] = [],
  ) {
    out.push({
      piece,
      profile: outline,
      holes,
      y,
      depth: d,
      z: Math.min(...outline.map((p) => p[1])),
      h: Math.max(...outline.map((p) => p[1])) - Math.min(...outline.map((p) => p[1])),
      kind: "profile",
      tone: "hi",
      lines: [],
      dots: [],
    });
  }
  function disc(piece: string, x: number, y: number, z: number, r: number, d: number) {
    profile(
      piece,
      Array.from({ length: 40 }, (_, i) => [
        x + r * Math.cos((i * Math.PI) / 20),
        z + r * Math.sin((i * Math.PI) / 20),
      ]),
      y,
      d,
    );
  }
  function screw(piece: string, x: number, z: number, y = depth / 2) {
    disc(piece, x, y, z, 1.5, 1);
    box(piece, x - 0.8, y + 1, z - 0.18, 1.6, 0.15, 0.36);
  }
  function face(piece: string, x: number, z: number, w: number, h: number, y = depth / 2) {
    box(piece, x, y, z, w, 2, h);
    for (const xx of [x + 3, x + w - 3])
      for (const zz of [z + 3, z + h - 3]) screw(piece, xx, zz, y + 2);
  }

  /* Base: a split support follows the logo's feet instead of hiding them on a slab. */
  for (const x of [-172, -104, 92, 160]) {
    box("stand", x, -depth / 2 - 3, -10, 12, depth + 6, 6);
    box("stand", x + 2, -depth / 2, -4, 8, depth, 4);
  }
  /* Steel ties bridge the logo's negative spaces, each end landing on a band. */
  for (const side of [-1, 1])
    for (const y of [-depth / 2 + 3, depth / 2 - 7]) {
      profile(
        "core",
        [
          [side * 144, 88],
          [side * 136, 88],
          [side * 120, 128],
          [side * 128, 128],
        ],
        y,
        4,
      );
      profile(
        "app",
        [
          [side * 92, 208],
          [side * 84, 208],
          [side * 64, 248],
          [side * 72, 248],
        ],
        y,
        4,
      );
      for (const [x, z] of [
        [side * 138, 88],
        [side * 124, 128],
        [side * 86, 208],
        [side * 68, 248],
      ])
        box(z > 200 ? "app" : "core", x - 4, y - 1, z - 2, 8, 6, 4);
    }

  for (const band of bands) {
    profile(band.piece, band.outline, -depth / 2, depth);
    for (const [x, z] of band.outline) {
      const cx = band.outline.reduce((s, p) => s + p[0], 0) / 4,
        cz = band.outline.reduce((s, p) => s + p[1], 0) / 4;
      screw(band.piece, x * 0.88 + cx * 0.12, z * 0.86 + cz * 0.14);
    }
  }

  for (const x of [-140, 92]) {
    const piece = x < 0 ? "go" : "foundation";
    face(piece, x, 20, 48, 32);
    box(piece, x + 12, depth / 2 + 3, 29, 24, 6, 4);
  }
  for (const x of [-72, -22, 28]) {
    face("core", x, 149, 42, 38);
    for (let i = 0; i < 5; i++) {
      box("core", x + i * 8, depth / 2 + 2, 141, 3, 3, 8);
      box("core", x + i * 8, depth / 2 + 2, 187, 3, 3, 8);
    }
  }
  for (const x of [-84, 84])
    for (let i = 0; i < 4; i++) box("core", x + i * 2, depth / 2 + 0.2, 136, 0.6, 0.5, 62);
  face("app", -29, 267, 58, 40);
  disc("app", 0, depth / 2 + 2, 287, 13, 5);
  for (let i = 0; i < 24; i++) {
    const a = (i * Math.PI) / 12;
    disc("app", Math.cos(a) * 10, depth / 2 + 7, 287 + Math.sin(a) * 10, 0.45, 0.25);
  }
  return out;
}
