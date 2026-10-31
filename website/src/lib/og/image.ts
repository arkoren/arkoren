import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { CollectionEntry } from "astro:content";
import { blogDate, getBlogPosts } from "../blog/posts";
import { getGuides, guidePlace, type Guide } from "../docs/guides";
import type { SocialImage } from "../site/seo";
import { LOGO_FILE, renderCard as draw, type CardText } from "./render";

/**
 * Social cards: one rose PNG per blog post and guide, drawn from the
 * page's own title so a shared link says what it points to. Pages ask for their
 * card with the builders below, and the /og/ endpoint renders the same cards,
 * so the URL in a page's metadata always has a matching image.
 */

/** What a card shows. Every field feeds the URL hash, so a change gets a new URL. */
export interface SocialCard extends CardText {
  /** Path below /og/, without extension, such as "blog/introducing-arkoren". */
  key: string;
}

/** A rendered size. Blog posts add Google's 16:9, 4:3, and 1:1 variants. */
export interface CardSize {
  suffix: string;
  width: number;
  height: number;
}

/** The size every platform reads from og:image and twitter:image. */
export const SHARE_SIZE: CardSize = { suffix: "", width: 1200, height: 630 };

/** Article images for Google, which recommends 16:9, 4:3, and 1:1. */
export const ARTICLE_SIZES: CardSize[] = [
  { suffix: "-16x9", width: 1200, height: 675 },
  { suffix: "-4x3", width: 1200, height: 900 },
  { suffix: "-1x1", width: 1200, height: 1200 },
];

/** The drawing's source, fonts, and logo feed every hash, so a design change gets new URLs. */
const TEMPLATE = createHash("sha256")
  .update(readFileSync(resolve(process.cwd(), "src/lib/og/render.ts")))
  .update(readFileSync(resolve(process.cwd(), "src/assets/fonts/og/Geist-SemiBold.ttf")))
  .update(readFileSync(resolve(process.cwd(), "src/assets/fonts/og/MapleMono-Bold.ttf")))
  .update(readFileSync(LOGO_FILE))
  .digest("hex");

/** The site path of one card size, with a content hash that changes with the card. */
export function cardPath(card: SocialCard, size: CardSize = SHARE_SIZE): string {
  const hash = createHash("sha256")
    .update(JSON.stringify([TEMPLATE, card, size]))
    .digest("hex")
    .slice(0, 10);
  return `/og/${card.key}${size.suffix}-${hash}.png`;
}

/** Alternative text names the card's content, not its decoration. */
export const cardAlt = (card: SocialCard) => `${card.title}. ${card.label}, Arkoren.`;

/** The og:image of a page that shares this card. */
export const cardImage = (card: SocialCard): SocialImage => ({
  src: cardPath(card),
  alt: cardAlt(card),
  width: SHARE_SIZE.width,
  height: SHARE_SIZE.height,
  type: "image/png",
});

export function blogCard(post: CollectionEntry<"blog">): SocialCard {
  return {
    key: `blog/${post.id}`,
    label: `Blog · ${post.data.category}`,
    title: post.data.title,
    footer: `${blogDate(post.data.publishedAt)} · ${post.data.author.name}`,
    footerMono: true,
  };
}

export function guideCard(entry: Guide, sectionTitle: string): SocialCard {
  return {
    key: `guides/${entry.id}`,
    label: `Guide · ${sectionTitle}`,
    title: entry.data.title,
    footer: entry.data.description,
    footerMono: false,
  };
}

/** Every card the site links to, for the /og/ endpoint. */
export async function allCards(): Promise<{ card: SocialCard; size: CardSize }[]> {
  const cards: { card: SocialCard; size: CardSize }[] = [];
  for (const post of await getBlogPosts()) {
    if (post.data.ogImage) continue;
    for (const size of [SHARE_SIZE, ...ARTICLE_SIZES]) cards.push({ card: blogCard(post), size });
  }
  for (const entry of await getGuides()) {
    const { section } = await guidePlace(entry);
    cards.push({ card: guideCard(entry, section.data.title), size: SHARE_SIZE });
  }
  return cards;
}

/** Renders one card size; the cache is keyed by the same hash as its URL. */
export const renderCard = (card: SocialCard, size: CardSize) =>
  draw(card, size, cardPath(card, size).slice("/og/".length).replaceAll("/", "__"));
