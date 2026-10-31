import { defineHastPlugin, type HastVisitorContext } from "satteri";
import type { Element, ElementContent } from "hast";

/** Status words stay readable in Markdown and become line icons on the site. */
const STATUS_PATHS: Record<string, string> = {
  Yes: "M5 12l4 4L19 6",
  No: "M6 6l12 12M18 6L6 18",
  Unknown: "M9 8a3 3 0 116 0c0 2-3 2-3 5M12 17v.01",
  Partial: "M12 3a9 9 0 100 18 9 9 0 000-18Z",
};

/** Narrow tree children to elements before inspecting table structure. */
function isElement(node: ElementContent): node is Element {
  return node.type === "element";
}

/** A portable status word can carry an optional, useful qualification. */
interface Status {
  /** Accessible name of the status icon. */
  label: string;
  /** Context shown on demand instead of repeating the status word. */
  detail?: string;
}

/** Parse a whole-cell status, optionally followed by an em dash and detail. */
function cellStatus(cell: Element): Status | undefined {
  if (cell.children.length !== 1 || cell.children[0].type !== "text") return undefined;
  const match = /^(Yes|No|Unknown|Partial)(?: — (.+))?$/.exec(cell.children[0].value.trim());
  return match === null ? undefined : { label: match[1], detail: match[2] };
}

/** Only qualified statuses become controls; ordinary icons add no tab stops. */
function statusIcon({ label, detail }: Status): Element {
  return {
    type: "element",
    tagName: detail === undefined ? "span" : "button",
    properties: {
      className: ["status"],
      ariaLabel: label,
      ...(detail === undefined
        ? { role: "img" }
        : {
            type: "button",
            "data-status-detail": detail,
            ariaDescription: detail,
          }),
    },
    children: [
      {
        type: "element",
        tagName: "svg",
        properties: {
          width: 18,
          height: 18,
          viewBox: "0 0 24 24",
          fill: "none",
          stroke: "currentColor",
          strokeWidth: "1.8",
          strokeLinecap: "round",
          strokeLinejoin: "round",
          ariaHidden: "true",
        },
        children: [
          {
            type: "element",
            tagName: "path",
            properties: { d: STATUS_PATHS[label] },
            children: [],
          },
          ...(label === "Partial"
            ? [
                {
                  type: "element" as const,
                  tagName: "path",
                  properties: { d: "M12 3a9 9 0 000 18Z", fill: "currentColor", stroke: "none" },
                  children: [],
                },
              ]
            : []),
        ],
      },
      {
        type: "element",
        tagName: "span",
        properties: { className: ["sr-only"] },
        children: [{ type: "text", value: label }],
      },
    ],
  };
}

/** Recognize matrices by their status-only data columns, not published wording. */
function renderStatuses(table: Readonly<Element>, ctx: HastVisitorContext): void {
  const rows = table.children
    .filter(isElement)
    .flatMap((section) => section.children.filter(isElement))
    .filter((row) => row.tagName === "tr");
  const body = rows.filter((row) =>
    row.children.some((cell) => isElement(cell) && cell.tagName === "td"),
  );
  if (body.length === 0) return;
  const cells = body.map((row) => row.children.filter(isElement));
  if (
    !cells.every(
      (row) => row.length > 1 && row.slice(1).every((cell) => cellStatus(cell) !== undefined),
    )
  )
    return;
  ctx.setProperty(table, "className", ["status-matrix"]);
  for (const row of rows) {
    for (const cell of row.children.filter(isElement)) {
      if (cell.tagName === "th") ctx.setProperty(cell, "scope", "col");
    }
  }
  for (const row of cells) {
    ctx.replaceNode(row[0], {
      ...row[0],
      tagName: "th",
      properties: { ...row[0].properties, scope: "row" },
    });
    for (const cell of row.slice(1)) {
      const status = cellStatus(cell);
      if (status !== undefined) ctx.replaceNode(cell.children[0], statusIcon(status));
    }
  }
}

/**
 * Keep native table layout inside a full-width horizontal scroll container.
 * Build-time wrappers work without JavaScript and allow keyboard scrolling.
 */
export const tablesPlugin = defineHastPlugin({
  name: "arkoren-tables",
  element: {
    filter: ["table"],
    visit(node, ctx) {
      renderStatuses(node, ctx);
      ctx.wrapNode(node, {
        type: "element",
        tagName: "div",
        properties: {
          className: ["table-scroll"],
          tabIndex: 0,
          role: "region",
          ariaLabel: "Scrollable table",
        },
        children: [],
      });
    },
  },
});
