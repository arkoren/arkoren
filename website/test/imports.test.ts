import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";

/**
 * Exercise the real discovery code in a fresh process for each partial checkout.
 * Module-level environment and import-path caches cannot leak between cases.
 * Fixtures use invented module names and contain both roots and public packages;
 * assertions protect catalog completeness and landing-page discovery together.
 * All files are isolated from the working website and removed even on failure.
 */
function assertCatalog(roots: string[], expected: string[]) {
  /** Prefer the approved session temp area when running under OpenCode. */
  const temporary = existsSync(resolve(tmpdir(), "opencode"))
    ? resolve(tmpdir(), "opencode")
    : tmpdir();
  /** This fixture owns only its temporary repository, never website caches. */
  const repository = mkdtempSync(resolve(temporary, "arkoren-import-catalog-"));
  try {
    for (const root of roots) {
      mkdirSync(resolve(repository, root, "fixturepkg"), { recursive: true });
      writeFileSync(resolve(repository, root, "go.mod"), `module arkoren.dev/${root}\n`);
      writeFileSync(resolve(repository, root, "fixturepkg/package.go"), "package fixturepkg\n");
    }
    /** Absolute source URLs allow discovery to run with the fixture as cwd. */
    const script = `
      import { libraryModules, importPaths } from ${JSON.stringify(new URL("../src/lib/docs/imports.ts", import.meta.url).href)};
      import { apiModules } from ${JSON.stringify(new URL("../src/lib/docs/api.ts", import.meta.url).href)};
      console.log(JSON.stringify({
        modules: libraryModules(),
        links: apiModules().flatMap(group => group.modules.map(module => module.path)).sort(),
        paths: importPaths().map(item => item.path),
      }));
    `;
    /** Compare roots and navigation independently against the case's expectation. */
    const result: { modules: string[]; links: string[]; paths: string[] } = JSON.parse(
      execFileSync(
        process.execPath,
        ["--experimental-strip-types", "--input-type=module", "-e", script],
        {
          cwd: repository,
          env: { ...process.env, ARKOREN_REPO: repository },
          encoding: "utf8",
          timeout: 10000,
        },
      ),
    );
    assert.deepEqual(result.modules, expected);
    assert.deepEqual(result.links, expected);
    for (const root of roots) {
      assert.ok(result.paths.includes(`arkoren.dev/${root}`), root);
      assert.ok(result.paths.includes(`arkoren.dev/${root}/fixturepkg`), root);
    }
    if (roots.length === 0) assert.deepEqual(result.paths, []);
  } finally {
    rmSync(repository, { recursive: true, force: true });
  }
}

test("a website-only checkout has an empty catalog and no import landing pages", () => {
  assertCatalog([], []);
});

test("foundation libraries are discoverable before core is imported", () => {
  assertCatalog(["foundation/fixture"], ["arkoren.dev/foundation/fixture"]);
});

test("foundation and core are discoverable without any vendor drivers", () => {
  assertCatalog(
    ["foundation/fixture", "core"],
    ["arkoren.dev/core", "arkoren.dev/foundation/fixture"],
  );
});

test("selected drivers add their links without requiring the other vendors or CLI", () => {
  assertCatalog(
    ["foundation/fixture", "core", "driver/fixture"],
    ["arkoren.dev/core", "arkoren.dev/driver/fixture", "arkoren.dev/foundation/fixture"],
  );
});

test("the CLI publishes import paths but never becomes a library catalog entry", () => {
  assertCatalog(
    ["foundation/fixture", "core", "driver/fixture", "cmd/arkoren"],
    ["arkoren.dev/core", "arkoren.dev/driver/fixture", "arkoren.dev/foundation/fixture"],
  );
});
