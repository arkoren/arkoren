import { fileURLToPath } from "node:url";
import { defineMdastPlugin } from "satteri";
import { resolveLink } from "../lib/docs/links";
import { REFERENCES_ROOT } from "../lib/docs/references";

/**
 * Adapt shared guides without changing website-only blog content. The layout
 * supplies the page title; relative Markdown links target HTML guide routes.
 * @example
 * satteri({ mdastPlugins: [referencesPlugin] });
 */
export const referencesPlugin = defineMdastPlugin({
  name: "arkoren-references",
  before(root, ctx) {
    const file = ctx.fileURL && fileURLToPath(ctx.fileURL);
    if (!file || !file.startsWith(`${REFERENCES_ROOT}/`)) return;
    const h1 = root.children.find((node) => node.type === "heading" && node.depth === 1);
    if (h1) ctx.removeNode(h1);
  },
  link(node, ctx) {
    if (!ctx.fileURL) return;
    const file = fileURLToPath(ctx.fileURL);
    if (!file.startsWith(`${REFERENCES_ROOT}/`)) return;
    const url = resolveLink(file, node.url);
    if (url) ctx.setProperty(node, "url", url);
  },
});
