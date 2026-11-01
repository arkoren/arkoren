import type { Loader } from "astro/loaders";
import { readReleases, releaseModules } from "./content.ts";

/**
 * Store validated plain Markdown without compiling it through MDX or the site's
 * trusted-author pipeline. Readers use renderReleaseBody, not content render().
 */
export function releasesLoader(): Loader {
  return {
    name: "published-module-releases",
    async load({ store, parseData }) {
      const entries = readReleases({
        root: process.env.ARKOREN_RELEASES,
        modules: releaseModules(),
      });
      const parsed = await Promise.all(
        entries.map(async (entry) => ({
          ...entry,
          data: await parseData({ id: entry.id, data: entry.data }),
        })),
      );
      store.clear();
      for (const entry of parsed) store.set(entry);
    },
  };
}
