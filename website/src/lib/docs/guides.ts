import { readFile } from "node:fs/promises";
import { getCollection, type CollectionEntry } from "astro:content";
import { resolveLink } from "./links";
import { REFERENCES_ROOT, SITE } from "./references";
import { buildGuideTree } from "./tree";

/** A rendered shared guide; obtain entries with getGuides(). @example const guide = (await getGuides())[0]; */
export type Guide = CollectionEntry<"guides">;

/** A guide folder and its reading order. @example const section = (await getGuideTree())[0]; */
export interface GuideSection {
  /** Folder metadata. @example console.log(section.meta.data.title); */
  meta: Guide;
  /** Pages in reading order, excluding metadata. @example section.pages.map(guideRoute); */
  pages: Guide[];
}

/**
 * Group guides by their authored metadata and reading order. Empty groups are omitted.
 * @example const links = (await getGuideTree()).flatMap(({ pages }) => pages.map(guideRoute));
 */
export async function getGuideTree(): Promise<GuideSection[]> {
  return buildGuideTree(await getCollection("guides"));
}

/**
 * Collect consecutive groups under their authored sidebar headings.
 * @example console.log((await getGuideSections())[0].title);
 */
export async function getGuideSections(): Promise<{ title: string; groups: GuideSection[] }[]> {
  const sections: { title: string; groups: GuideSection[] }[] = [];
  for (const group of await getGuideTree()) {
    const title = group.meta.data.section ?? "";
    if (sections.at(-1)?.title === title) sections.at(-1)!.groups.push(group);
    else sections.push({ title, groups: [group] });
  }
  return sections;
}

/** Read guide pages without group metadata. @example (await getGuides()).map(guideRoute); */
export async function getGuides(): Promise<Guide[]> {
  return (await getGuideTree()).flatMap((s) => s.pages);
}

/**
 * Locate an existing guide's group and reading-order neighbours.
 * @example const { next } = await guidePlace((await getGuides())[0]);
 */
export async function guidePlace(entry: Guide) {
  const tree = await getGuideTree();
  const order = await getGuides();
  const at = order.findIndex((g) => g.id === entry.id);
  const section = tree.find((s) => s.pages.some((g) => g.id === entry.id))!;
  return { section: section.meta, previous: order[at - 1], next: order[at + 1] };
}

/** Use the short navigation title when supplied. @example guideLabel((await getGuides())[0]); */
export const guideLabel = (g: Guide) => g.data.nav ?? g.data.title;
/** Guide HTML URL, independent of navigation labels. @example guideRoute((await getGuides())[0]); */
export const guideRoute = (g: Guide) => `/docs/guides/${guideSlug(g)}`;
/** Authored group/page identifier. @example guideSlug((await getGuides())[0]); */
export const guideSlug = (g: Guide) => g.id;

/**
 * Read a shared guide verbatim, retaining frontmatter. File errors propagate.
 * @example const source = await readSource("guides", "first-steps/quickstart");
 */
export const readSource = (collection: "guides", id: string) =>
  readFile(`${REFERENCES_ROOT}/${collection}/${id}.md`, "utf8");

/**
 * Remove guide frontmatter and make links absolute for offline archives.
 * File errors propagate rather than publishing incomplete exports.
 * @example const body = await readBundled("guides", "first-steps/quickstart");
 */
export async function readBundled(collection: "guides", id: string): Promise<string> {
  const file = `${REFERENCES_ROOT}/${collection}/${id}.md`;
  const body = (await readFile(file, "utf8")).replace(/^---\n[\s\S]*?\n---\n/, "");
  return body.replace(/\]\(([^)\s]+)\)/g, (match, url: string) => {
    const route = resolveLink(file, url, true);
    return route ? `](${route.startsWith("/") ? SITE + route : route})` : match;
  });
}

/**
 * Format an absolute raw-guide link for selective agent discovery.
 * @example llmsItem("Quickstart", "/docs/guides/first-steps/quickstart", "Build a service.");
 */
export const llmsItem = (title: string, route: string, description: string) =>
  `- [${title}](${SITE}${route}.md): ${description.replace(/\s+/g, " ").trim()}`;
