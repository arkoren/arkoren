import type { APIRoute, GetStaticPaths } from "astro";
import { getGuides, guideSlug, readSource } from "../../../lib/docs/guides";
import { SITE } from "../../../lib/docs/references";

/** Serves each source file with a link back to its canonical HTML documentation. */
export const getStaticPaths = (async () =>
  (await getGuides()).map((e) => ({
    params: { slug: guideSlug(e) },
    props: { id: e.id },
  }))) satisfies GetStaticPaths;

export const GET: APIRoute = async ({ props }) =>
  new Response(
    `${await readSource("guides", props.id)}\n\n[HTML documentation](${SITE}/docs/guides/${props.id})\n`,
    {
      headers: {
        "Content-Type": "text/markdown; charset=utf-8",
        Link: `<${SITE}/docs/guides/${props.id}>; rel="canonical"`,
      },
    },
  );
