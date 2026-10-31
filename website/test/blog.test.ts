import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import test from "node:test";
import { required } from "./dom.ts";
import {
  blogStateURL,
  nextBlogSort,
  normalizeBlogText,
  readBlogState,
  selectBlogPosts,
} from "../src/lib/blog/state.ts";

/** Distinct publication dates, categories, and body text exercise combined controls. */
const posts = [
  {
    id: "welcome",
    title: "Welcome",
    category: "Announcements",
    publishedAt: 10,
    text: "Welcome to Arkoren",
  },
  {
    id: "release",
    title: "A release",
    category: "Changelogs",
    publishedAt: 30,
    text: "Redis driver shutdown improved",
  },
  {
    id: "design",
    title: "Boundaries",
    category: "Engineering notes",
    publishedAt: 20,
    text: "Redis contracts and lifecycle",
  },
];

test("an empty URL selects the complete archive, newest first", () => {
  const state = readBlogState(
    new URLSearchParams(),
    posts.map((post) => post.category),
  );
  assert.deepEqual(state, { q: "", category: "", sort: "newest" });
  assert.deepEqual(
    selectBlogPosts(posts, state).map((post) => post.id),
    ["release", "design", "welcome"],
  );
});

test("a shared URL restores search, category, and order together", () => {
  const state = readBlogState(new URLSearchParams("q=+REDIS+&category=Changelogs&sort=oldest"), [
    "Changelogs",
  ]);
  assert.deepEqual(state, { q: "REDIS", category: "Changelogs", sort: "oldest" });
  assert.deepEqual(
    selectBlogPosts(posts, state).map((post) => post.id),
    ["release"],
  );
});

test("unknown category and sort values fall back to valid controls", () => {
  assert.deepEqual(
    readBlogState(new URLSearchParams("category=missing&sort=invalid"), ["Announcements"]),
    {
      q: "",
      category: "",
      sort: "newest",
    },
  );
});

test("search matches all words across body text regardless of case or spacing", () => {
  const selected = selectBlogPosts(posts, {
    q: "  SHUTDOWN \n redis ",
    category: "",
    sort: "newest",
  });
  assert.deepEqual(
    selected.map((post) => post.id),
    ["release"],
  );
});

test("unmatched searches return an empty result", () => {
  assert.deepEqual(selectBlogPosts(posts, { q: "unfindable", category: "", sort: "newest" }), []);
});

test("category selection uses exact labels and does not depend on query text", () => {
  assert.deepEqual(
    selectBlogPosts(posts, { q: "", category: "Engineering notes", sort: "newest" }).map(
      (post) => post.id,
    ),
    ["design"],
  );
});

test("oldest ordering uses publication time without mutating the original list", () => {
  const selected = selectBlogPosts(posts, { q: "", category: "", sort: "oldest" });
  assert.deepEqual(
    selected.map((post) => post.id),
    ["welcome", "design", "release"],
  );
  assert.deepEqual(
    posts.map((post) => post.id),
    ["welcome", "release", "design"],
  );
});

test("alphabetical ordering uses titles rather than filenames", () => {
  assert.deepEqual(
    selectBlogPosts(posts, { q: "", category: "", sort: "title" }).map((post) => post.id),
    ["release", "design", "welcome"],
  );
});

test("the text order control switches from newest to oldest", () => {
  assert.equal(nextBlogSort("newest"), "oldest");
});

test("the text order control switches from oldest to alphabetical", () => {
  assert.equal(nextBlogSort("oldest"), "title");
});

test("the text order control wraps from alphabetical to newest", () => {
  assert.equal(nextBlogSort("title"), "newest");
});

test("equal titles and dates use stable ID ordering", () => {
  const tied = [
    { id: "b", title: "Same", category: "Notes", publishedAt: 10, text: "" },
    { id: "a", title: "Same", category: "Notes", publishedAt: 10, text: "" },
  ];
  assert.deepEqual(
    selectBlogPosts(tied, { q: "", category: "", sort: "newest" }).map((post) => post.id),
    ["a", "b"],
  );
  assert.deepEqual(
    selectBlogPosts(tied, { q: "", category: "", sort: "title" }).map((post) => post.id),
    ["a", "b"],
  );
});

test("URL state safely encodes text and preserves unrelated parameters and fragments", () => {
  const url = blogStateURL(new URL("https://arkoren.dev/blog?utm_source=share#top"), {
    q: "cache & <script>",
    category: "News & notes",
    sort: "title",
  });
  assert.equal(url.searchParams.get("q"), "cache & <script>");
  assert.equal(url.searchParams.get("category"), "News & notes");
  assert.equal(url.searchParams.get("sort"), "title");
  assert.equal(url.searchParams.get("utm_source"), "share");
  assert.equal(url.hash, "#top");
});

test("reset removes only archive state and default values stay out of URLs", () => {
  const original = new URL("https://arkoren.dev/blog?q=redis&category=Notes&sort=oldest&other=1");
  const url = blogStateURL(original, { q: "", category: "", sort: "newest" });
  assert.equal(url.href, "https://arkoren.dev/blog?other=1");
  assert.equal(original.searchParams.get("q"), "redis");
});

test("normalization handles Unicode compatibility characters and repeated whitespace", () => {
  assert.equal(normalizeBlogText("  ＡＲＫＯＲＥＮ\n Notes  "), "arkoren notes");
});

test("the built archive derives category selectors from its posts and includes full-text search data", () => {
  const html = readFileSync(new URL("../dist/blog.html", import.meta.url), "utf8");
  const cards = html.match(/<article\b[^>]*data-blog-post[^>]*>/g) ?? [];
  assert.ok(cards.length > 0);
  const categories = new Set(
    cards.map((card) => required(card.match(/data-category="([^"]+)"/))[1]),
  );
  const list = required(html.match(/<nav\b[^>]*aria-label="Post categories"[\s\S]*?<\/nav>/))[0];
  const values = [...list.matchAll(/data-blog-category="([^"]+)"/g)].map((match) => match[1]);
  assert.deepEqual(
    values,
    [...categories].sort((a, b) => a.localeCompare(b, "en")),
  );
  assert.ok(list.includes('data-blog-category=""'));
  assert.equal([...list.matchAll(/aria-current="true"/g)].length, 1);
  assert.ok(html.includes("data-search="));
  assert.ok(html.includes("<noscript>"));
});

test("every published post filename has a built post route", () => {
  const source = new URL("../src/content/blog/", import.meta.url);
  for (const filename of readdirSync(source, { recursive: true, encoding: "utf8" }).filter((file) =>
    /\.mdx?$/.test(file),
  )) {
    const markdown = readFileSync(new URL(filename, source), "utf8");
    if (/^draft:\s*true\s*$/m.test(markdown)) continue;
    const html = readFileSync(
      new URL(`../dist/blog/${filename.replace(/\.mdx?$/, ".html")}`, import.meta.url),
      "utf8",
    );
    assert.ok(html.includes("data-blog-body"));
    assert.equal([...html.matchAll(/<h1\b/g)].length, 1);
    assert.ok(html.includes('property="article:published_time"'));
    assert.ok(html.includes("data-pagefind-body"));
    const nav = required(html.match(/<header\b[^>]*data-header[\s\S]*?<\/header>/))[0];
    assert.match(nav, /<a\b[^>]*href="\/blog"[^>]*aria-current="page"/);
    assert.ok(nav.includes("data-mobile-nav"));
  }
});

test("post contents agree across navigation variants and mark one active location", () => {
  for (const filename of readdirSync(new URL("../dist/blog/", import.meta.url)).filter((file) =>
    file.endsWith(".html"),
  )) {
    const html = readFileSync(new URL(`../dist/blog/${filename}`, import.meta.url), "utf8");
    const nav = required(html.match(/<nav\b[^>]*aria-label="On this page"[\s\S]*?<\/nav>/))[0];
    const mobile = required(
      html.match(/<details\b[^>]*data-toc-mobile[^>]*>[\s\S]*?<\/details>/),
    )[0];
    assert.doesNotMatch(required(mobile.match(/<details[^>]*>/))[0], /\bopen\b/);
    assert.deepEqual(
      [...mobile.matchAll(/href="([^"]+)"/g)].map((match) => match[1]),
      [...nav.matchAll(/href="([^"]+)"/g)].map((match) => match[1]),
    );
    assert.equal([...nav.matchAll(/aria-current="location"/g)].length, 1);
    assert.ok(nav.includes('href="#top" aria-current="location"'));
  }
});

test("post header images reserve space and resolve to published local assets", () => {
  for (const filename of readdirSync(new URL("../dist/blog/", import.meta.url)).filter((file) =>
    file.endsWith(".html"),
  )) {
    const html = readFileSync(new URL(`../dist/blog/${filename}`, import.meta.url), "utf8");
    const header = required(html.match(/<header\b[^>]*data-blog-header[\s\S]*?<\/header>/))[0];
    const banner = html.match(/<figure\b[^>]*data-banner[\s\S]*?<\/figure>/)?.[0] ?? "";
    if (banner.includes("<img")) {
      assert.match(banner, /\bloading="eager"/);
      assert.match(banner, /\bfetchpriority="high"/);
    }
    for (const [image] of banner.matchAll(/<svg\b[^>]*>/g)) {
      assert.match(image, /\bwidth="[1-9]\d*"/);
      assert.match(image, /\bheight="[1-9]\d*"/);
      assert.match(image, /\baria-label="[^"]+"/);
    }
    for (const [image] of (header + banner).matchAll(/<img\b[^>]*>/g)) {
      const src = required(image.match(/\bsrc="([^"]+)"/))[1];
      assert.ok(src.startsWith("/_astro/"), `${filename}: expected a bundled image URL`);
      assert.ok(
        existsSync(new URL(`../dist${src}`, import.meta.url)),
        `${filename}: image asset is missing`,
      );
      assert.match(image, /\bwidth="[1-9]\d*"/);
      assert.match(image, /\bheight="[1-9]\d*"/);
    }
  }
});
