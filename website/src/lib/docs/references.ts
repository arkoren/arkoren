import { resolve } from "node:path";

/**
 * Absolute path to the single authored guide tree embedded by the CLI.
 * ARKOREN_REFERENCES can supply an isolated fixture tree for build tests.
 * @example const file = `${REFERENCES_ROOT}/guides/first-steps/quickstart.md`;
 */
export const REFERENCES_ROOT = resolve(
  process.env.ARKOREN_REFERENCES ?? "../cmd/arkoren/skills/arkoren/references",
);

/**
 * Production origin for absolute agent links, independent of preview hosts.
 * @example const indexURL = `${SITE}/llms.txt`;
 */
export const SITE = "https://arkoren.dev";

/**
 * GitHub source directory for skill files that have no rendered website route.
 * @example const skillURL = `${SKILL_ON_GITHUB}/SKILL.md`;
 */
export const SKILL_ON_GITHUB =
  "https://github.com/arkoren/arkoren/blob/main/cmd/arkoren/skills/arkoren";
