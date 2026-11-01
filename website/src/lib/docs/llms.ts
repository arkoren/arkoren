import {
  getGuideSections,
  getGuides,
  guideRoute,
  llmsItem,
  readBundled,
  type GuideSection,
} from "./guides";
import { apiModules } from "./api";
import { SITE } from "./references";
import { renderArchive } from "./archive";

/** Shared introduction, independent of release or indexing availability. */
const SUMMARY = "Arkoren is a modular Go toolkit with portable contracts and opt-in drivers.";

/** Render a group's selective raw-guide links, preserving authored reading order. */
const sectionItems = (section: GuideSection) =>
  section.pages
    .map((guide) => llmsItem(guide.data.title, guideRoute(guide), guide.data.description))
    .join("\n");

/** Navigation labels affect grouping, never public Markdown URLs. */
async function guideSections(): Promise<string> {
  return (await getGuideSections())
    .map(
      (section) =>
        `## ${section.title}\n\n` +
        section.groups
          .map((group) => `### ${group.meta.data.title}\n\n${sectionItems(group)}`)
          .join("\n\n"),
    )
    .join("\n\n");
}

/** Module-only external reference links shared by both agent indexes. */
function moduleSections(): string {
  const groups = apiModules();
  if (groups.length === 0) return "No library modules are published yet.";
  return groups
    .map(
      (group) =>
        `## ${group.title}\n\n` +
        group.modules.map((module) => `- [${module.path}](${module.url})`).join("\n"),
    )
    .join("\n\n");
}

/** Canonical API notice states retirement and availability without network checks. */
const API_NOTICE =
  "pkg.go.dev is the canonical Go API reference. Links identify library modules, not release or indexing availability; unpublished or unindexed modules may return not found. Local API Markdown and bulk API exports are retired.";

/**
 * Build one guide group's selective agent index.
 * @example const text = await sectionIndex((await getGuideTree())[0]);
 */
export async function sectionIndex(section: GuideSection): Promise<string> {
  return (
    [
      `# Arkoren guides: ${section.meta.data.title}`,
      `> ${section.meta.data.description}`,
      sectionItems(section),
      `## Optional\n\n- [All guides](${SITE}/docs/guides/llms.txt)\n- [API module catalog](${SITE}/docs/api/llms.txt)`,
    ].join("\n\n") + "\n"
  );
}

/**
 * List guide links grouped by their authored sidebar sections.
 * @example const text = await guidesIndex();
 */
export async function guidesIndex(): Promise<string> {
  return (
    [
      "# Arkoren guides",
      `> ${SUMMARY}`,
      await guideSections(),
      `## Optional\n\n- [Bulk guides archive](${SITE}/docs/guides/llms-full.txt): offline indexing or deliberate large-context use, not the default reading path\n- [API module catalog](${SITE}/docs/api/llms.txt)`,
    ].join("\n\n") + "\n"
  );
}

/**
 * List canonical module references, never local declarations or package pages.
 * @example const text = await apiIndex();
 */
export async function apiIndex(): Promise<string> {
  return (
    [
      "# Arkoren API reference",
      API_NOTICE,
      moduleSections(),
      `## Guides\n\n- [Guides index](${SITE}/docs/guides/llms.txt)`,
    ].join("\n\n") + "\n"
  );
}

/**
 * Combine selective guide discovery and canonical external API links.
 * @example const text = await rootIndex();
 */
export async function rootIndex(): Promise<string> {
  return (
    [
      "# Arkoren",
      `> ${SUMMARY}`,
      `[Documentation website](${SITE}/docs) · [Use with your agent](${SITE}/docs/guides/agent-skills/working-with-agents.md)`,
      "For coding tasks, fetch only the guides you need. Full guide archives are optional bulk exports, not the recommended reading path.",
      await guideSections(),
      API_NOTICE,
      moduleSections(),
      `## Optional\n\n- [Bulk guides archive](${SITE}/docs/guides/llms-full.txt): offline indexing or deliberate large-context use\n- [Guides index](${SITE}/docs/guides/llms.txt)\n- [API module catalog](${SITE}/docs/api/llms.txt)\n- [Changelog](${SITE}/changelog): published module versions and release notes`,
    ].join("\n\n") + "\n"
  );
}

/** Concatenate only shared guides; API declarations are owned by pkg.go.dev. */
async function guideArchive(heading: string, indexPath: string): Promise<string> {
  const pages = await Promise.all(
    (await getGuides()).map(async (guide) => {
      const body = (await readBundled("guides", guide.id)).trim();
      const withTitle = /^# /m.test(body.split("\n", 1)[0])
        ? body
        : `# ${guide.data.title}\n\n${body}`;
      return `Source: ${SITE}${guideRoute(guide)}.md\n[HTML documentation](${SITE}${guideRoute(guide)})\n\n${withTitle}`;
    }),
  );
  return renderArchive({ heading, indexURL: SITE + indexPath, pages });
}

/**
 * Export guides for deliberate offline indexing, with the bulk-use warning.
 * @example const text = await guidesFull();
 */
export async function guidesFull() {
  return guideArchive("Arkoren guides", "/docs/guides/llms.txt");
}

/**
 * Preserve the root archive URL as a guides-only export; no API bundle remains.
 * @example const text = await rootFull();
 */
export async function rootFull() {
  return guideArchive("Arkoren guides", "/llms.txt");
}
