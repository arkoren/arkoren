import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { Resvg } from "@resvg/resvg-js";
import satori from "satori";

/**
 * Draws a social card as a PNG: the landing card's rose grid, logo, and type,
 * with the page's label, title, and footer. Kept free of Astro imports so the
 * drawing can be previewed with plain Node.
 */

/** What a card shows. */
export interface CardText {
  label: string;
  title: string;
  footer: string;
  footerMono: boolean;
}

/** Output dimensions in pixels. */
export interface CardBox {
  width: number;
  height: number;
}

/** Fonts ship with the site (SIL OFL), so cards render the same on every machine. */
const FONTS = resolve(process.cwd(), "src/assets/fonts/og");
let fonts: Parameters<typeof satori>[1]["fonts"] | undefined;
const loadFonts = () =>
  (fonts ??= [
    {
      name: "Geist",
      data: readFileSync(resolve(FONTS, "Geist-SemiBold.ttf")),
      weight: 600,
      style: "normal",
    },
    {
      name: "Maple Mono",
      data: readFileSync(resolve(FONTS, "MapleMono-Bold.ttf")),
      weight: 700,
      style: "normal",
    },
  ]);

const ROSE = "#c70036";
const CREAM = "#fffbf5";
/** The site's logo file: the rose mark on its warm-white square, used as is. */
export const LOGO_FILE = resolve(process.cwd(), "src/assets/images/logo/logo-rose-on-white.svg");
const LOGO = `data:image/svg+xml;base64,${readFileSync(LOGO_FILE).toString("base64")}`;

/** A satori element; children may be text or further elements. */
type Node = { type: string; props: Record<string, unknown> };
const el = (
  type: string,
  style: Record<string, unknown>,
  children?: unknown,
  props: Record<string, unknown> = {},
): Node => ({ type, props: { style, children, ...props } });

/** Longer titles step down in size so they stay within three lines. */
const titleSize = (title: string) => (title.length > 64 ? 64 : title.length > 36 ? 76 : 88);

/** The card as satori elements: the landing card's rose grid, logo, and type. */
function drawing(card: CardText, size: CardBox): Node {
  const mono = {
    fontFamily: "Maple Mono",
    fontWeight: 700,
    letterSpacing: 1,
    textTransform: "uppercase",
  };
  const line = { overflow: "hidden", whiteSpace: "nowrap", textOverflow: "ellipsis" };
  const inner = {
    width: size.width - 180,
    height: size.height - 124,
    marginTop: 70,
    marginLeft: 90,
  };
  return el(
    "div",
    {
      width: size.width,
      height: size.height,
      display: "flex",
      color: CREAM,
      backgroundColor: ROSE,
      backgroundImage:
        "linear-gradient(to right, rgba(255,255,255,.1) 1px, transparent 1px), linear-gradient(to bottom, rgba(255,255,255,.1) 1px, transparent 1px)",
      backgroundSize: "24px 24px",
      fontFamily: "Geist",
    },
    [
      el(
        "div",
        { ...inner, display: "flex", flexDirection: "column", justifyContent: "space-between" },
        [
          el(
            "div",
            {
              display: "flex",
              width: "100%",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 32,
            },
            [
              el("div", { display: "flex", flexShrink: 0, alignItems: "center", gap: 20 }, [
                el("img", { width: 64, height: 64 }, undefined, {
                  src: LOGO,
                  width: 64,
                  height: 64,
                }),
                el("div", { fontSize: 40, fontWeight: 600, letterSpacing: -1.5 }, "arkoren"),
              ]),
              el("div", { ...mono, ...line, minWidth: 0, fontSize: 24, opacity: 0.85 }, card.label),
            ],
          ),
          el(
            "div",
            {
              display: "block",
              width: "100%",
              fontSize: titleSize(card.title),
              fontWeight: 600,
              lineHeight: 1.1,
              letterSpacing: -3,
              lineClamp: 3,
              overflow: "hidden",
              textWrap: "balance",
            },
            card.title,
          ),
          el("div", { display: "flex", width: "100%", flexDirection: "column", gap: 28 }, [
            el("div", { width: "100%", height: 1, backgroundColor: "rgba(255,251,245,.3)" }),
            el(
              "div",
              {
                display: "flex",
                width: "100%",
                alignItems: "center",
                justifyContent: "space-between",
                gap: 40,
              },
              [
                el(
                  "div",
                  card.footerMono
                    ? { ...mono, ...line, minWidth: 0, fontSize: 26 }
                    : { ...line, minWidth: 0, fontSize: 30, opacity: 0.85 },
                  card.footer,
                ),
                el(
                  "div",
                  { ...mono, fontSize: 26, flexShrink: 0, textTransform: "none" },
                  "arkoren.dev ↗",
                ),
              ],
            ),
          ]),
        ],
      ),
    ],
  );
}

/** Renders are cached by URL hash, so rebuilds only draw cards that changed. */
const CACHE = resolve(process.cwd(), "node_modules/.astro/og");

/** Draws the card, or reads it from the cache under `cacheKey` (a file name). */
export async function renderCard(
  card: CardText,
  size: CardBox,
  cacheKey?: string,
): Promise<Buffer> {
  const cached = cacheKey && resolve(CACHE, cacheKey);
  if (cached && existsSync(cached)) return readFileSync(cached);
  const svg = await satori(drawing(card, size) as never, {
    width: size.width,
    height: size.height,
    fonts: loadFonts(),
  });
  // Satori already turned the text into paths, so skip resvg's slow system font scan.
  const png = new Resvg(svg, { fitTo: { mode: "original" }, font: { loadSystemFonts: false } })
    .render()
    .asPng();
  if (cached) {
    mkdirSync(CACHE, { recursive: true });
    writeFileSync(cached, png);
  }
  return png;
}
