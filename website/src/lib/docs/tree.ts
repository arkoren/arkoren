/** Metadata required to place a guide in its source-folder navigation group. */
interface GuideMetadata {
  /** Source-relative identifier without the Markdown extension. */
  id: string;
  /** Labels and sort keys supplied by the guide frontmatter. */
  data: {
    /** Full page or group title. */
    title: string;
    /** Numeric reading order within the parent collection. */
    order: number;
  };
}

/** Build folder groups in metadata order, omitting groups without pages. */
export function buildGuideTree<T extends GuideMetadata>(all: T[]): { meta: T; pages: T[] }[] {
  /** Compare frontmatter order, using the title to break ties. */
  const byOrder = (a: T, b: T) =>
    a.data.order - b.data.order || a.data.title.localeCompare(b.data.title);
  return all
    .filter((entry) => entry.id.split("/").length === 2 && entry.id.endsWith("/index"))
    .sort(byOrder)
    .map((meta) => ({
      meta,
      pages: all
        .filter(
          (entry) =>
            entry.id.split("/")[0] === meta.id.split("/")[0] && !entry.id.endsWith("/index"),
        )
        .sort(byOrder),
    }))
    .filter((group) => group.pages.length > 0);
}
