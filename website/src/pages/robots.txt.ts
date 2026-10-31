import type { APIRoute } from "astro";

/** Permit public search and agent crawlers and advertise the generated sitemap. */
export const GET: APIRoute = ({ site }) =>
  new Response(
    `User-agent: *\nAllow: /\n\nSitemap: ${new URL("/sitemap-index.xml", site).href}\n`,
    { headers: { "Content-Type": "text/plain; charset=utf-8" } },
  );
