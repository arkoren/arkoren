import { existsSync, readdirSync } from "node:fs";

/**
 * Independently enumerate library roots for rendered catalog assertions.
 * Only manifests in canonical library areas count; command documentation and
 * nested packages must not manufacture entries. Missing areas contribute none.
 * @returns Sorted canonical import paths, including an empty website checkout.
 * @example
 * assert.deepEqual(renderedLinks, checkoutModules().map((path) => `https://pkg.go.dev/${path}`));
 */
export function checkoutModules(): string[] {
  /** Resolve from this file so Playwright's fixture cwd cannot change the inventory. */
  const repository = new URL("../../", import.meta.url);
  /** Foundation and vendor areas can arrive independently, in any order. */
  const areas = ["foundation", "driver"].filter((area) =>
    existsSync(new URL(`${area}/`, repository)),
  );
  /** Core is one module; the other library areas contain direct module children. */
  const roots = [
    ...(existsSync(new URL("core/go.mod", repository)) ? ["core"] : []),
    ...areas.flatMap((area) =>
      readdirSync(new URL(`${area}/`, repository))
        .filter((name) => existsSync(new URL(`${area}/${name}/go.mod`, repository)))
        .map((name) => `${area}/${name}`),
    ),
  ];
  return roots.map((root) => `arkoren.dev/${root}`).sort();
}
