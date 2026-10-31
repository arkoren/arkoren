import type { APIRoute, GetStaticPaths } from "astro";
import { getGuideTree } from "../../../../lib/docs/guides";
import { sectionIndex } from "../../../../lib/docs/llms";

/** `/docs/guides/<section>/llms.txt`, one per guide section. */
export const getStaticPaths = (async () =>
  (await getGuideTree()).map((section) => ({
    params: { section: section.meta.id.split("/")[0] },
    props: { section },
  }))) satisfies GetStaticPaths;

export const GET: APIRoute = async ({ props }) =>
  new Response(await sectionIndex(props.section), {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
