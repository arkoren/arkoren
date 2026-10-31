/** Shareable archive controls, independent of the DOM and content loader. */
export interface BlogState {
  /** Whitespace-trimmed, case-insensitive search across post text. */
  q: string;
  /** Exact category label, or an empty string for every category. */
  category: string;
  /** Stable ordering with a route-based tie breaker. */
  sort: "newest" | "oldest" | "title";
}

/** Search metadata shared by pre-rendered cards and the archive controller. */
export interface BlogRecord {
  /** Unique collection ID, used for deterministic ordering. */
  id: string;
  /** Visible title, also used by the alphabetical sort. */
  title: string;
  /** Exact frontmatter category. */
  category: string;
  /** Publication timestamp as milliseconds since the Unix epoch. */
  publishedAt: number;
  /** Title, description, category, author, and body text. */
  text: string;
}

/** Cycle the archive's text control through every supported ordering. */
export function nextBlogSort(sort: BlogState["sort"]): BlogState["sort"] {
  if (sort === "newest") return "oldest";
  if (sort === "oldest") return "title";
  return "newest";
}

/** Normalize spacing and case consistently for content and search queries. */
export function normalizeBlogText(text: string): string {
  return text.normalize("NFKC").toLowerCase().replace(/\s+/gu, " ").trim();
}

/** Ignore unknown categories and sort values when opening a shared URL. */
export function readBlogState(params: URLSearchParams, categories: readonly string[]): BlogState {
  const category = params.get("category") ?? "";
  const sort = params.get("sort");
  return {
    q: (params.get("q") ?? "").trim(),
    category: categories.includes(category) ? category : "",
    sort: sort === "oldest" || sort === "title" ? sort : "newest",
  };
}

/** Keep unrelated query parameters and fragments; omit default controls. */
export function blogStateURL(current: URL, state: BlogState): URL {
  const url = new URL(current);
  for (const key of ["q", "category", "sort"]) url.searchParams.delete(key);
  if (state.q !== "") url.searchParams.set("q", state.q);
  if (state.category !== "") url.searchParams.set("category", state.category);
  if (state.sort !== "newest") url.searchParams.set("sort", state.sort);
  return url;
}

/** Match all query words and the category, then sort without mutating input. */
export function selectBlogPosts<T extends BlogRecord>(posts: readonly T[], state: BlogState): T[] {
  const words = normalizeBlogText(state.q)
    .split(" ")
    .filter((word) => word !== "");
  return posts
    .filter((post) => {
      const text = normalizeBlogText(post.text);
      return (
        (state.category === "" || post.category === state.category) &&
        words.every((word) => text.includes(word))
      );
    })
    .sort((a, b) => {
      if (state.sort === "title") {
        return a.title.localeCompare(b.title, "en") || a.id.localeCompare(b.id, "en");
      }
      const difference =
        state.sort === "oldest" ? a.publishedAt - b.publishedAt : b.publishedAt - a.publishedAt;
      return difference || a.id.localeCompare(b.id, "en");
    });
}
