import { defineCollection } from "astro:content";
import { glob } from "astro/loaders";
import { z } from "astro/zod";
import { REFERENCES_ROOT } from "./lib/docs/references";
import { releaseModules, releaseSchema } from "./lib/releases/content";
import { releasesLoader } from "./lib/releases/loader";

/** Entry ids mirror the file path without `.md`, so ids map 1:1 onto routes. */
const generateId = ({ entry }: { entry: string }) => entry.replace(/\.md$/, "");

/**
 * Human guides, one level of groups: `references/guides/<group>/<page>.md`. A
 * group's `index.md` holds only its title, description, order, and `section`
 * (the sidebar heading it sits under). `nav` is an optional shorter sidebar
 * label; `title` is the page heading. Source folders determine navigation groups
 * and public URLs.
 */
const guides = defineCollection({
  /** Compile through Vite so renderer edits cannot reuse persisted collection HTML. */
  loader: glob({
    base: `${REFERENCES_ROOT}/guides`,
    pattern: "**/*.md",
    generateId,
    deferRender: true,
  }),
  schema: z.object({
    title: z.string().trim().min(1),
    description: z.string().trim().min(1),
    order: z.number(),
    nav: z.string().optional(),
    section: z.string().optional(),
  }),
});

/** Website-only posts, in Markdown or MDX; the optional path override isolates build-test fixtures. */
const blog = defineCollection({
  loader: glob({
    base: process.env.ARKOREN_BLOG ?? "./src/content/blog",
    pattern: "**/*.{md,mdx}",
    deferRender: true,
  }),
  schema: ({ image }) => {
    /** The byline's details; only the name is required. */
    const author = z.object({
      name: z.string().trim().min(1),
      /** Muted role shown beneath the name. */
      title: z.string().trim().min(1).optional(),
      /** A page that identifies the author, such as a personal site or profile. */
      url: z.url().optional(),
      /** Portrait path relative to the post. */
      avatar: image().optional(),
      /** Linked company appended after the role. */
      company: z
        .object({
          name: z.string().trim().min(1),
          url: z.url().optional(),
        })
        .optional(),
    });
    return z.object({
      title: z.string().trim().min(1),
      description: z.string().trim().min(1),
      publishedAt: z.coerce.date(),
      /** A short personal aside, separate from the article summary. */
      authorDesk: z.string().trim().min(1).optional(),
      /** A glimpse into the article's construction or supporting material. */
      behindPost: z
        .object({
          title: z.string().trim().min(1),
          text: z.string().trim().min(1),
        })
        .optional(),
      /** Annotated references displayed in the desktop-only editorial margin. */
      readingShelf: z
        .array(
          z.object({
            title: z.string().trim().min(1),
            description: z.string().trim().min(1),
            href: z
              .string()
              .refine((value) => /^\/(?!\/)/.test(value) || /^https?:\/\//.test(value)),
          }),
        )
        .default([]),
      /** Curated next steps; allow only local paths and HTTP(S) links. */
      explore: z
        .array(
          z.object({
            title: z.string().trim().min(1),
            description: z.string().trim().min(1),
            href: z
              .string()
              .refine((value) => /^\/(?!\/)/.test(value) || /^https?:\/\//.test(value)),
          }),
        )
        .default([]),
      updatedAt: z.coerce.date().optional(),
      category: z.string().trim().min(1),
      /** Who wrote the post: a name, or the byline's details. Defaults to Arkoren. */
      author: z
        .union([
          z
            .string()
            .trim()
            .min(1)
            .transform((name): z.infer<typeof author> => ({ name })),
          author,
        ])
        .default({ name: "Arkoren" }),
      banner: z
        .object({
          src: image(),
          alt: z.string().trim().min(1),
          width: z.number().int().positive(),
          height: z.number().int().positive(),
        })
        .optional(),
      /** Replaces the generated social card; a 1200 × 630 PNG or JPEG. */
      ogImage: z
        .object({
          src: image(),
          alt: z.string().trim().min(1),
        })
        .optional(),
      draft: z.boolean().default(false),
    });
  },
});

/** Published module history is website-only, never a blog or embedded guide. */
const releases = defineCollection({
  loader: releasesLoader(),
  schema: releaseSchema(releaseModules()),
});

export const collections = { guides, blog, releases };
