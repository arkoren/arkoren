import { dirname, relative, resolve } from "node:path";
import { REFERENCES_ROOT, SKILL_ON_GITHUB } from "./references";

/**
 * Resolves a relative Markdown link found in `sourceFile` to the URL it should
 * have on the website.
 *
 * Links to pages under `references/guides/` become site
 * routes (`/docs/guides/x`) with the `.md` suffix removed
 * and the `#fragment` kept; pass `raw: true` to keep `.md` so the link targets
 * the raw Markdown route instead. Links to other skill files such as `SKILL.md`
 * point at GitHub, so they never 404.
 *
 * Returns `undefined` for links that must be left alone: absolute URLs, site
 * paths, bare fragments, and relative links that do not end in `.md`.
 *
 * @example
 * resolveLink(`${REFERENCES_ROOT}/guides/testing/overview.md`, "./fakes.md");
 * // "/docs/guides/testing/fakes"
 */
export function resolveLink(sourceFile: string, url: string, raw = false): string | undefined {
  if (/^([a-z][a-z0-9+.-]*:|\/|#)/i.test(url)) return undefined;
  const [path, fragment] = url.split("#", 2);
  if (!path.endsWith(".md")) return undefined;

  const target = resolve(dirname(sourceFile), path);
  const rel = relative(REFERENCES_ROOT, target).split("\\").join("/");
  const hash = fragment ? `#${fragment}` : "";
  if (/^guides\//.test(rel)) return `/docs/${raw ? rel : rel.slice(0, -3)}${hash}`;
  return `${SKILL_ON_GITHUB}/${relative(resolve(REFERENCES_ROOT, ".."), target).split("\\").join("/")}${hash}`;
}
