import { defineHastPlugin } from "satteri";

/** GitHub alert markers and the class each one maps to. */
const KINDS = {
  NOTE: "note",
  TIP: "tip",
  IMPORTANT: "important",
  WARNING: "warning",
  CAUTION: "caution",
} as const;
const MARKER = /^\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/;

/**
 * Sätteri (hast) plugin that renders GitHub alerts as callouts.
 *
 * A blockquote whose first paragraph starts with `[!NOTE]`, `[!TIP]`,
 * `[!IMPORTANT]`, `[!WARNING]`, or `[!CAUTION]` (the syntax GitHub renders, so
 * the skill files read well there and offline) gets the classes `callout` and
 * `callout-<kind>`, and the marker is removed from its text. The label is drawn
 * by CSS, so the HTML stays a plain blockquote for readers without styles.
 */
export const calloutsPlugin = defineHastPlugin({
  name: "arkoren-callouts",
  element: {
    filter: ["blockquote"],
    visit(node, ctx) {
      const paragraph = node.children.find((child) => child.type === "element");
      if (!paragraph || paragraph.type !== "element" || paragraph.tagName !== "p") return;
      const text = paragraph.children[0];
      if (!text || text.type !== "text") return;
      const match = MARKER.exec(text.value);
      if (!match) return;

      ctx.setProperty(node, "className", [
        "callout",
        `callout-${KINDS[match[1] as keyof typeof KINDS]}`,
      ]);
      const rest = text.value.slice(match[0].length);
      if (rest === "") ctx.removeNode(text);
      else ctx.setProperty(text, "value", rest);
      // A marker on its own line leaves a line break before the first sentence.
      const next = paragraph.children[1];
      if (rest === "" && next?.type === "element" && next.tagName === "br") ctx.removeNode(next);
    },
  },
});

/**
 * Sätteri (hast) plugin that renders a table-cell link whose whole text is
 * "Docs" as a small button, so catalog tables (such as the drivers table) can
 * link each row to its page without showing the long target. In plain
 * Markdown it stays an ordinary link.
 *
 * @example
 * // Configure Astro's Markdown processor with the generated table treatment.
 * satteri({ hastPlugins: [tableButtonsPlugin] });
 */
export const tableButtonsPlugin = defineHastPlugin({
  name: "arkoren-table-buttons",
  element: {
    filter: ["a"],
    visit(node, ctx) {
      const parent = ctx.parent(node);
      if (
        parent.type !== "element" ||
        parent.tagName !== "td" ||
        ctx.textContent(node).trim() !== "Docs"
      )
        return;
      ctx.setProperty(node, "className", ["table-button"]);
    },
  },
});
