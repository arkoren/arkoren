/** The mark's three bands describe ownership boundaries, not a mandatory runtime. */
export const parts = [
  {
    id: "go",
    title: "Standard Go",
    object: "Building blocks · standard library",
    text: "Start with the standard library you already know. Your handlers stay HTTP handlers, your contexts carry cancellation, and your logger stays slog.",
    relation: "No new application model to learn. Use Go directly wherever it fits.",
    examples: ["net/http", "context", "log/slog"],
    detail:
      "Your application can call the standard library directly; Core is not a mandatory stop along the way.",
    linked: ["app"],
    href: "https://pkg.go.dev/std",
  },
  {
    id: "foundation",
    title: "Foundations",
    object: "Building blocks · independent modules",
    text: "Independent standard-library-only modules: validation, SQL queries, collections, problem details, keys and certificates. Use them without the rest of Arkoren.",
    relation: "Building blocks that do not import Core or your app.",
    examples: ["Validation", "SQL queries", "Problem details"],
    detail:
      "Install a foundation module on its own. There is no Core dependency and no requirement to adopt the rest of the toolkit.",
    linked: ["core", "app"],
    href: "/foundation",
  },
  {
    id: "core",
    title: "Arkoren Core",
    object: "The middle band",
    text: "Focused packages for HTTP, application lifecycle, service contracts, events and typed MCP tools. Construct the pieces you need; vendor drivers attach through contracts and live in separately installed modules.",
    relation: "Libraries your app calls, not a runtime that takes over your app.",
    examples: ["HTTP", "Service contracts", "Lifecycle"],
    detail:
      "Vendor drivers plug into service contracts here. Your application chooses and installs each driver separately.",
    linked: ["foundation", "app"],
    href: "/docs/guides/core-concepts/facades-and-drivers",
  },
  {
    id: "app",
    title: "Your application",
    object: "The top band",
    text: "Your handlers, domain rules and explicit composition. You choose dependencies, register operations and own startup and cleanup. Arkoren's application coordinator is optional.",
    relation: "Your code makes the connections and keeps control.",
    examples: ["Domain rules", "Handlers", "Explicit wiring"],
    detail:
      "Compose Standard Go, Foundations and Core in the combination you need. The shape describes ownership, not a required dependency stack.",
    linked: ["foundation", "core"],
    href: "/docs/guides/application/composition",
  },
] as const;
