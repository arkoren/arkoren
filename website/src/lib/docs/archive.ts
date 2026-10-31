/** Inputs for an exhaustive documentation export, in its existing page order. */
interface Archive {
  /** Human-readable scope of the export. */
  heading: string;
  /** Absolute URL of the index for selective reading. */
  indexURL: string;
  /** Complete rendered pages, including their source links. */
  pages: string[];
}

/**
 * Preserve every page while putting bulk-use guidance before the content.
 * Size describes the documentation body, excluding this generated notice.
 * The character heuristic is deliberately not presented as a tokenizer count.
 */
export function renderArchive({ heading, indexURL, pages }: Archive): string {
  const body = pages.join("\n\n---\n\n") + "\n";
  const bytes = new TextEncoder().encode(body).length;
  const estimatedTokens = Math.ceil(body.length / 4);
  const number = new Intl.NumberFormat("en-US");
  return [
    `# ${heading}`,
    "> Bulk documentation archive — not the recommended starting point for coding tasks.",
    "This exhaustive export is intended for offline indexing, bulk processing, or deliberate " +
      "large-context use. Loading it all can exhaust an agent's context window and increase processing cost.",
    `Start with [the documentation index](${indexURL}) and fetch only the pages your task needs.`,
    `Documentation content: ${number.format(pages.length)} pages; ${number.format(bytes)} UTF-8 bytes; ` +
      `approximately ${number.format(estimatedTokens)} tokens ` +
      "(rough estimate: characters / 4, not a model tokenizer; actual counts vary by model).",
    "---",
    body,
  ].join("\n\n");
}
