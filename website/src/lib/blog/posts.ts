import { getCollection, type CollectionEntry } from "astro:content";
import { normalizeBlogText, selectBlogPosts, type BlogRecord } from "./state";

/** One canonical list excludes drafts from routes, category counts, and search. */
export async function getBlogPosts(): Promise<CollectionEntry<"blog">[]> {
  const posts = await getCollection("blog", ({ data }) => !data.draft);
  const ordered = selectBlogPosts(
    posts.map((post) => ({ ...blogRecord(post), post })),
    {
      q: "",
      category: "",
      sort: "newest",
    },
  );
  return ordered.map(({ post }) => post);
}

/** A collection filename determines the post URL; keep it stable after publishing. */
export function blogRoute(post: CollectionEntry<"blog">): string {
  return `/blog/${post.id.split("/").map(encodeURIComponent).join("/")}`;
}

/** Build full-text search once, without shipping a runtime Markdown renderer. */
export function blogRecord(post: CollectionEntry<"blog">): BlogRecord {
  return {
    id: post.id,
    title: post.data.title,
    category: post.data.category,
    publishedAt: post.data.publishedAt.valueOf(),
    text: normalizeBlogText(
      [
        post.data.title,
        post.data.description,
        post.data.category,
        post.data.author.name,
        post.body ?? "",
      ].join(" "),
    ),
  };
}

/** UTC avoids moving date-only frontmatter to a different day on the build host. */
export function blogDate(date: Date): string {
  return new Intl.DateTimeFormat("en", {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone: "UTC",
  }).format(date);
}

/** An approximate reading time includes prose and code, with a one-minute floor. */
export function readingMinutes(post: CollectionEntry<"blog">): number {
  return Math.max(1, Math.ceil((post.body ?? "").split(/\s+/u).filter(Boolean).length / 220));
}
