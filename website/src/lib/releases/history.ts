import { releaseIdentity, type ReleaseData } from "./content.ts";

/** Group independently versioned modules by UTC publication day, newest first. */
export function releaseHistory<T extends { data: ReleaseData }>(entries: readonly T[]) {
  const groups = new Map<string, T[]>();
  const sorted = [...entries].sort((left, right) => {
    const date = right.data.publishedAt.localeCompare(left.data.publishedAt);
    return date || releaseIdentity(left.data).localeCompare(releaseIdentity(right.data));
  });
  for (const entry of sorted) {
    const day = entry.data.publishedAt.slice(0, 10);
    const group = groups.get(day) ?? [];
    group.push(entry);
    groups.set(day, group);
  }
  return [...groups].map(([day, releases]) => ({ day, releases }));
}

/** Display publication dates in UTC, independently of the builder's locale. */
export function releaseDate(publishedAt: string): string {
  return new Intl.DateTimeFormat("en", {
    dateStyle: "long",
    timeZone: "UTC",
  }).format(new Date(publishedAt));
}

/** Pin the actual module version; Git tag directory prefixes are not Go versions. */
export function releaseCommand(data: ReleaseData): string {
  const verb = data.module === "arkoren.dev/cmd/arkoren" ? "install" : "get";
  return `go ${verb} ${data.module}@${data.version}`;
}
