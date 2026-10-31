# Page Patterns

## Contents

- [Universal page order](#universal-page-order)
- [Onboarding and quickstarts](#onboarding-and-quickstarts)
- [Concept guides](#concept-guides)
- [Task and operations guides](#task-and-operations-guides)
- [Catalogs and references](#catalogs-and-references)
- [Choosing and comparing paths](#choosing-and-comparing-paths)

## Universal Page Order

Use the skill's writing settings before selecting a pattern. Package usage is
the default; a package overview is not an onboarding tutorial. Omit authored
contents lists unless explicitly requested, regardless of page length. When
requested, mirror actual headings; the website already supplies section navigation.

Most pages should move in this order:

1. **Introduction:** establish the reader's situation and purpose, then introduce the capability and primary interface; follow the [use-case-first pattern](prose-style.md#paragraph-rhythm).
2. **Mental model or prerequisite:** distinguish terms and establish required state.
3. **Baseline operation:** show a focused example using the prerequisites already introduced.
4. **Common variations:** add the options most readers will need.
5. **Advanced and operational behavior:** cover performance, lifecycle, integration, or customization.
6. **Failures and operational detail:** deepen the explanation where needed; essential limits and safety conditions already belong beside the action they qualify.
7. **Reference material or next steps:** provide lookup tables and direct related links.

These stages are optional shapes, not subjects to fill. Include a variation or
operational detail when it answers the reader's next relevant question; keep
constraints beside the action they qualify. A reference page can open with a
one-sentence contract; an installation page needs prerequisites first. Reach the
first useful result or distinction without an unrelated feature tour. Use the
[attention and progress guidance](prose-style.md#attention-and-progress) to support
both scanning and continuous reading.

Use heading depth deliberately:

```text
## Connections                    # a major concept or phase
### Creating a connection         # a reader action or a focused concept
#### Custom connection names      # a narrow variation or exception
```

Keep sibling headings grammatically parallel. Prefer `Creating`, `Inspecting`, and `Deleting` over `Create`, `Inspection`, and `How to Delete`.

## Onboarding and Quickstarts

Use this pattern only for an explicitly requested onboarding lesson or an
existing page whose stated purpose is a quickstart. Do not select it merely
because a package can coordinate a whole application.

A quickstart is a complete, confidence-building first success, not a compressed reference.

Use this sequence:

1. State the runtime, account, extension, service, or local-tool prerequisite.
2. Give the exact installation or generation command.
3. Show the file or object that the command creates, when that context matters.
4. Build one small end-to-end path through focused steps, not one large code block.
5. Explain what each step contributes after it is shown.
6. Give a visible checkpoint: URL, output, response, generated file, or assertion.
7. Link the immediate next concept; do not append an unrelated feature list.

Use explicit temporal cues for a walkthrough:

````markdown
First, create a handler:

```shell
tool make:handler ReportHandler
```

Next, register the handler with the application:

```ts
app.use(new ReportHandler());
```

Now, start the service and visit `http://localhost:3000/reports`.
````

State generated versus pre-existing resources. “If this directory does not exist, the command will create it” removes uncertainty without adding a detour.

When platforms or tools differ, introduce the default route first. Put equivalent variants in labeled subsections or a concise GFM table. Shared documentation must remain readable offline without website-only tabs or components. Never let an optional helper look mandatory.

## Concept Guides

Explain when the capability is useful, then how it works in an existing
application. A focused example illustrates the distinction; it does not need
to construct a complete application. Use tutorial-style cumulative setup only
when requested.

1. Establish the practical need with the [use-case-first pattern](prose-style.md#paragraph-rhythm).
2. Define the concept in plain language and explain where it participates in the system.
3. Show a minimal example that uses it.
4. Interpret the code in terms of the model.
5. Explain conventions and automatic behavior.
6. Add customization or alternative patterns.
7. Cover design trade-offs and testability where they affect implementation.

Use a direct distinction when two concepts are easy to confuse:

```markdown
A connection identifies the remote service. A channel is a named path inside
that connection. One connection may expose several channels.
```

After a sample, explain the important line rather than walking through every token:

```markdown
The container resolves `ReportStore` when it creates `ReportService`. Because
`SqlReportStore` is registered for that dependency, the service receives the SQL
implementation without constructing it directly.
```

Use a rhetorical question sparingly when it introduces a real design decision: “When should you register a custom implementation?” Answer it immediately with a concrete criterion.

## Task and Operations Guides

Write operational sections as runbooks. The reader must know the state before and after every command.

```text
Goal → prerequisite → command/configuration → expected state → verification
     → routine variations → failure handling → production constraint
```

For each command or configuration change, state:

- where to run it or which file to edit;
- whether it creates, modifies, reloads, clears, or deletes state;
- whether it is safe to repeat;
- what it affects in development versus production;
- how to observe success;
- what to do if it fails or is inappropriate for the environment.

Use the aggregate operation before granular controls when both exist. Then explain when granular control matters:

````markdown
To refresh all generated artifacts, run:

```shell
tool optimize
```

If you only changed routes, run `tool routes:cache` instead. This avoids
rebuilding unrelated artifacts during a focused deployment step.
````

Place production and security consequences adjacent to the relevant action. Do not hide them in a distant “Production” section.

## Catalogs and References

Use a catalog when the reader is looking up stable names, locations, or supported methods.

For directory, configuration, or component catalogs:

- organize from broad scope to narrow scope;
- give each entry a repeated one-paragraph shape: location → contents → when it exists → how it is created or changed;
- distinguish defaults, generated resources, and optional resources;
- use a table only when the fields are uniform enough to scan.

For a large method surface:

1. introduce the type and its conventions;
2. provide an alphabetical or task-grouped linked index only when explicitly requested;
3. use one canonical entry per method;
4. link aliases to the canonical entry rather than duplicate prose;
5. order each entry from contract to example to edge behavior.

Use stable anchors that match the identifier. This makes links durable and keeps the page usable as a lookup tool.

## Choosing and Comparing Paths

When several paths solve similar problems, teach the decision before the syntax.

| Need | Recommend | Explain |
| --- | --- | --- |
| Need the total result count | full pagination | It performs a count query to calculate page totals. |
| Only need next/previous navigation | simple pagination | It avoids the extra count query. |
| Need efficient traversal of a large ordered set | cursor iteration | It retains less state but requires a stable ordering. |

State the default or recommended choice first. Explain the cost, prerequisite, or lost behavior of each alternative. Follow the comparison with dedicated sections only when the reader needs implementation details.

Avoid false symmetry. If one approach is secure, supported, or appropriate only under a narrow condition, say so plainly.
