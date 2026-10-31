import type { APIRoute, GetStaticPaths } from "astro";
import { allCards, cardPath, renderCard, type CardSize, type SocialCard } from "../../lib/og/image";

/** One PNG per social card; pages link to the same paths through cardPath. */
export const getStaticPaths = (async () =>
  (await allCards()).map(({ card, size }) => ({
    params: { file: cardPath(card, size).slice("/og/".length) },
    props: { card, size },
  }))) satisfies GetStaticPaths;

export const GET: APIRoute<{ card: SocialCard; size: CardSize }> = async ({ props }) =>
  new Response(new Uint8Array(await renderCard(props.card, props.size)), {
    headers: { "Content-Type": "image/png" },
  });
