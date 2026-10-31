/** A Schema.org node with JSON-compatible property values. */
export type StructuredData = {
  [key: string]: string | number | boolean | StructuredData | StructuredData[] | string[];
};

/** One visible breadcrumb, shared with its structured-data representation. */
export interface Breadcrumb {
  /** Human-readable navigation label. */
  name: string;
  /** Public page or section URL, relative to the site when local. */
  href: string;
}

/** The image a page shares through og:image and twitter:image. */
export interface SocialImage {
  /** Absolute URL, or a path resolved against the site. */
  src: string;
  alt: string;
  width: number;
  height: number;
  /** MIME type, such as image/png. */
  type: string;
}

/** Return the public HTML URL without build suffixes, filters, or fragments. */
export function canonicalURL(path: string, site: URL | string): URL {
  const url = new URL(path, site);
  url.pathname = url.pathname.replace(/\/index\.html$/, "/").replace(/\.html$/, "");
  if (url.pathname !== "/") url.pathname = url.pathname.replace(/\/$/, "");
  url.search = "";
  url.hash = "";
  return url;
}

/** Keep text containing HTML closing tags inert inside a JSON-LD script. */
export function serializeStructuredData(data: StructuredData): string {
  return JSON.stringify(data).replaceAll("<", "\\u003c");
}

/** Describe the same ordered navigation that is visible above the content. */
export function breadcrumbData(items: Breadcrumb[], site: URL | string): StructuredData {
  return {
    "@type": "BreadcrumbList",
    itemListElement: items.map((item, index) => ({
      "@type": "ListItem",
      position: index + 1,
      name: item.name,
      item: new URL(item.href, site).href,
    })),
  };
}
