import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { Resvg } from "@resvg/resvg-js";

/** Render authored SVG masters with the site's bundled fonts, not host fonts. */
const root = new URL("../", import.meta.url);
const fonts = ["Geist-SemiBold.ttf", "MapleMono-Bold.ttf"].map((name) =>
  fileURLToPath(new URL(`src/assets/fonts/og/${name}`, root)),
);

/**
 * Render an SVG master using bundled fonts and embedded sibling artwork.
 * The optional width is in pixels; omitting it preserves source dimensions.
 * File and renderer failures propagate so asset generation cannot succeed
 * with missing artwork. The returned PNG buffer is owned by the caller.
 */
function png(source: URL, width?: number) {
  // Embed referenced SVGs so resvg uses the official artwork, not a copied path.
  const svg = readFileSync(source, "utf8").replace(
    /href="([^"]+\.svg)"/g,
    (_, href) =>
      `href="data:image/svg+xml;base64,${readFileSync(new URL(href, source)).toString("base64")}"`,
  );
  return new Resvg(svg, {
    fitTo: width === undefined ? { mode: "original" } : { mode: "width", value: width },
    font: { fontFiles: fonts, loadSystemFonts: false, defaultFontFamily: "Geist" },
  })
    .render()
    .asPng();
}

/** All logo exports and banner masters have matching, reproducible PNG siblings. */
for (const folder of ["logo", "blog", "og"]) {
  const directory = new URL(`src/assets/images/${folder}/`, root);
  for (const name of readdirSync(directory).filter((entry) => entry.endsWith(".svg"))) {
    const target = new URL(name.replace(/\.svg$/, ".png"), directory);
    writeFileSync(target, png(new URL(name, directory)));
    console.log(fileURLToPath(target));
  }
}

/** ICO directory entries point to PNG payloads for modern multi-size browsers. */
const favicon = new URL("public/favicon.svg", root);
const sizes = [16, 32, 48];
const images = sizes.map((size) => png(favicon, size));
const header = Buffer.alloc(6 + sizes.length * 16);
header.writeUInt16LE(1, 2);
header.writeUInt16LE(sizes.length, 4);
let offset = header.length;
for (const [index, size] of sizes.entries()) {
  const entry = 6 + index * 16;
  header[entry] = size;
  header[entry + 1] = size;
  header.writeUInt16LE(1, entry + 4);
  header.writeUInt16LE(32, entry + 6);
  header.writeUInt32LE(images[index].length, entry + 8);
  header.writeUInt32LE(offset, entry + 12);
  offset += images[index].length;
}
writeFileSync(new URL("public/favicon.ico", root), Buffer.concat([header, ...images]));
writeFileSync(new URL("public/apple-touch-icon.png", root), png(favicon, 180));
console.log("Regenerated browser icons (16, 32, 48, and 180 px).");
