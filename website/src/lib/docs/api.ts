import { libraryModules } from "./imports.ts";

/**
 * Library modules in navigation order, discovered from local go.mod metadata.
 * Explicit /mod/ links select module landing pages, never package roots.
 * Package and tooling paths never become entries. Discovery does not assert
 * that a module has been released or indexed by pkg.go.dev. Groups without
 * modules in the checkout are omitted, so the list is empty until the Go
 * modules are published.
 * @example
 * console.log(apiModules()[0]?.modules[0].url);
 */
export function apiModules() {
  /** Only module roots from public library areas are eligible. */
  const modules = libraryModules();
  return [
    { id: "core", title: "Core", prefix: "arkoren.dev/core" },
    { id: "foundation", title: "Foundation", prefix: "arkoren.dev/foundation/" },
    { id: "drivers", title: "Vendor drivers", prefix: "arkoren.dev/driver/" },
  ]
    .map((group) => ({
      ...group,
      modules: modules
        .filter((path) => path.startsWith(group.prefix))
        .map((path) => ({ path, url: `https://pkg.go.dev/${path}` })),
    }))
    .filter((group) => group.modules.length > 0);
}
