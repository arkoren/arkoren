import mdx from "@astrojs/mdx";
import { satteri } from "@astrojs/markdown-satteri";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";
import { codeThemes } from "./src/lib/site/theme";
import { calloutsPlugin, tableButtonsPlugin } from "./src/plugins/callouts";
import { referencesPlugin } from "./src/plugins/references";
import { tablesPlugin } from "./src/plugins/tables";
import { legacyApiRedirects } from "./src/lib/docs/redirects";

// https://astro.build/config
export default defineConfig({
  site: "https://arkoren.dev",
  image: {
    service: {
      entrypoint: "astro/assets/services/sharp",
      /** Spend build time, not visitor bandwidth, on lossless PNGs and WebP. */
      config: { png: { compressionLevel: 9 }, webp: { effort: 6 } },
    },
  },
  // server: {
  // 	allowedHosts: ["buzz-otherwise-davis-receive.trycloudflare.com"],
  // },
  integrations: [
    // Blog posts may be .mdx to render site components, such as the contract grid.
    mdx(),
    sitemap({
      /**
       * Index canonical content, not import stubs, redirects, or raw bundles.
       * Keep moved HTML routes out; raw Markdown is excluded by extension.
       */
      filter: (page) => {
        const { pathname, search } = new URL(page);
        return (
          search === "" &&
          !/\.(?:md|txt|xml)$/.test(pathname) &&
          !pathname.startsWith("/docs/guides/http/") &&
          !pathname.startsWith("/docs/api/") &&
          !pathname.startsWith("/docs/guides/webhooks/") &&
          ![
            "/docs/api/core/app",
            "/docs/guides",
            "/docs/guides/events/asyncapi",
            "/docs/guides/payments/webhooks",
          ].includes(pathname) &&
          (pathname === "/" ||
            pathname === "/docs" ||
            pathname === "/blog" ||
            pathname.startsWith("/docs/") ||
            pathname.startsWith("/blog/"))
        );
      },
    }),
  ],
  // Guides retain raw Markdown twins; API reference links go to pkg.go.dev.
  vite: { plugins: [tailwindcss()] },
  trailingSlash: "never",
  build: { format: "file" },
  redirects: {
    ...legacyApiRedirects(),
    "/docs/api/llms-full.txt": "/docs/api/llms.txt",
    "/docs/guides": "/docs#guides",
    "/docs/guides/http/signed-links": "/docs/guides/http-capabilities/secure-links",
    "/docs/guides/http/signed-links.md": "/docs/guides/http-capabilities/secure-links.md",
    "/docs/guides/http/routing": "/docs/guides/http-fundamentals/routing",
    "/docs/guides/http/routing.md": "/docs/guides/http-fundamentals/routing.md",
    "/docs/guides/http/requests": "/docs/guides/http-fundamentals/requests",
    "/docs/guides/http/requests.md": "/docs/guides/http-fundamentals/requests.md",
    "/docs/guides/http/responses": "/docs/guides/http-fundamentals/responses",
    "/docs/guides/http/responses.md": "/docs/guides/http-fundamentals/responses.md",
    "/docs/guides/http/error-handling": "/docs/guides/http-fundamentals/error-handling",
    "/docs/guides/http/error-handling.md": "/docs/guides/http-fundamentals/error-handling.md",
    "/docs/guides/http/sessions": "/docs/guides/http-capabilities/sessions",
    "/docs/guides/http/sessions.md": "/docs/guides/http-capabilities/sessions.md",
    "/docs/guides/http/localization": "/docs/guides/http-capabilities/localization",
    "/docs/guides/http/localization.md": "/docs/guides/http-capabilities/localization.md",
    "/docs/guides/http/secure-links": "/docs/guides/http-capabilities/secure-links",
    "/docs/guides/http/secure-links.md": "/docs/guides/http-capabilities/secure-links.md",
    "/docs/guides/http/webhooks": "/docs/guides/http-capabilities/webhooks",
    "/docs/guides/http/webhooks.md": "/docs/guides/http-capabilities/webhooks.md",
    "/docs/guides/webhooks/receiving": "/docs/guides/http-capabilities/webhooks",
    "/docs/guides/webhooks/receiving.md": "/docs/guides/http-capabilities/webhooks.md",
    "/docs/guides/webhooks/providers": "/docs/guides/http-capabilities/webhooks#provider-adapters",
    "/docs/guides/webhooks/providers.md":
      "/docs/guides/http-capabilities/webhooks.md#provider-adapters",
    "/docs/guides/webhooks/llms.txt": "/docs/guides/llms.txt",
    "/docs/guides/payments/webhooks": "/docs/guides/payments/drivers",
    "/docs/guides/payments/webhooks.md": "/docs/guides/payments/drivers.md",
    "/docs/guides/http/server": "/docs/guides/http-networking/server",
    "/docs/guides/http/server.md": "/docs/guides/http-networking/server.md",
    "/docs/guides/http/http-client": "/docs/guides/http-networking/http-client",
    "/docs/guides/http/http-client.md": "/docs/guides/http-networking/http-client.md",
    "/docs/guides/http/llms.txt": "/docs/guides/llms.txt",
    "/docs/guides/http/validation": "/docs/guides/validation/overview",
    "/docs/guides/http/validation.md": "/docs/guides/validation/overview.md",
    "/docs/guides/http/openapi": "/docs/guides/openapi/overview",
    "/docs/guides/http/openapi.md": "/docs/guides/openapi/overview.md",
    "/docs/guides/events/asyncapi": "/docs/guides/asyncapi/overview",
    "/docs/guides/events/asyncapi.md": "/docs/guides/asyncapi/overview.md",
  },
  markdown: {
    processor: satteri({
      mdastPlugins: [referencesPlugin],
      hastPlugins: [calloutsPlugin, tableButtonsPlugin, tablesPlugin],
    }),
    shikiConfig: {
      themes: codeThemes,
    },
  },
});
