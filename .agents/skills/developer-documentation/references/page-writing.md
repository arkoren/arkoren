# Writing a Page

Use this workflow for every new or materially revised consumer guide. It captures
the category-by-category, page-by-page review used for Database Overview. Apply
the decisions, not that page's exact headings, example count, or subject matter.

## Contents

- [Category Review](#category-review)
- [Page Contract](#page-contract)
- [Section Review](#section-review)
- [Focused Examples](#focused-examples)
- [Page Summary](#page-summary)
- [Review and Delivery](#review-and-delivery)

## Category Review

For a category rewrite, settle its page map before drafting its pages. For a
single-page edit, inspect the category and incoming links to establish the page's
boundary; reopening the whole category is unnecessary unless that boundary fails.

1. Read the existing pages and inspect the public contracts, implementation, and
   tests relevant to their claims. Headings reveal candidates for review, not
   proof that a page is needed or that its claims are correct.
2. Identify the reader's distinct tasks and decisions. Separate subjects when
   they have different starting states or outcomes; merge pages when an ordinary
   task requires avoidable jumps between them. A package boundary or a long file
   alone does not justify a category or page.
3. Present the best-supported page map first, with short titles, a brief purpose
   for each page, and concrete evidence for consequential splits or merges.
   Challenge the existing organization rather than treating it as the default
   answer. Distinguish verified findings from unverified proposals.
4. Agree on the map with the user before restructuring a category. Review one
   page at a time in the agreed order; get feedback before moving to the next.
   If the user explicitly delegates a batch, apply the same review to each page.

Use responsibilities to establish boundaries. For example, Database covers
execution and result mapping; Queries covers statement construction; Migrations
covers schema evolution and deployment. Schema Builder stays with Migrations
while it serves that journey; technical independence alone does not require a
fourth category. This is an example of the decision, not a taxonomy to impose on
unrelated features.

When a reset to placeholders is explicitly authorized, keep valid metadata and
an honest unfinished notice. Preserve the original source in version history,
update moved-page links and navigation, and report missing sections, matrices,
and broken fragments as draft failures. A placeholder is not a reviewed guide.
Do not weaken publishing tests to make the incomplete category pass.

## Page Contract

Before outlining, state the following in the conversation, not in a new planning
file unless requested:

| Decision | Required answer |
| --- | --- |
| Reader | What does the reader already know or have configured? |
| Outcome | What can the reader understand, do, or decide after this page? |
| Scope | Which responsibility belongs here, and which sibling page owns adjacent setup or depth? |
| Evidence | Which declarations, implementation, and tests establish the behavior being taught? |
| Example | What ordinary application situation demonstrates the outcome with minimal setup? |

An overview introduces the capability's mental model and ordinary use, not
everything related to it. A facade overview can assume a configured facade and
operation context, linking driver-specific construction, credentials, pools,
connectivity, and lifecycle to Drivers. Include a constructor when constructing
the documented object is part of this page's outcome, not merely because every
package has one. Retain any ownership or safety condition necessary for the
shown operation even when its full setup belongs elsewhere.

## Section Review

Write the opening directly below the H1. Establish the practical need, define
the primary terms, and explain the relevant responsibility. An `Introduction`
heading is optional, not a required section.

For each proposed section, identify what the reader can understand, do, or decide
next and why it belongs at that position. Order sections around those needs, not
a list of API methods. Prerequisites and definitions precede their use; failures,
defaults, ownership, and constraints stay beside the affected operation.

Use short, descriptive headings and titles: aim for two or three words; four is
acceptable when needed for meaning. Keep official names and identifiers intact.
Use Title Case, parallel sibling phrasing, and a hierarchy no deeper than H4.
For example, `Reading Data`, `Executing Statements`, and `Data Mapping` identify
distinct responsibilities without sentence-length labels. A context-clear label
such as `Overview` need not repeat the category name.

Read the page continuously after outlining. Each paragraph should establish the
next action, explain its result, or answer a relevant question. Move unrelated
depth to its owning page or API reference. Do not append lifecycle, testing,
performance, or advanced sections just to complete a standard template. Keep
required safety beside the action instead of relocating it with optional depth.

## Focused Examples

Code blocks must be small and teach one operation or decision. When a larger
example is necessary, break it into meaningful steps with explanatory prose
between blocks. Define an input, build a statement, and execute it separately
when each step teaches something. Do not split mechanically by line count or
compress code to satisfy a size target.

Each teaching unit follows **situation → action → small snippet → result**:

- Choose an ordinary application task. Reuse its entities and identifiers when
  that makes the explanation coherent; do not turn every guide into a cumulative
  application tutorial.
- Name existing collaborators, import paths, tables, and contexts in prose or a
  nearby small block. State schema assumptions before querying. Construction,
  HTTP routing, and lifecycle wiring stay elsewhere unless they are the lesson.
- Prefer a small named struct for a typed application input, such as a validated
  HTTP request, rather than an ad hoc `map[string]any` literal. Use the actual
  public adapter required by the API: `query.Values(input)` converts a struct for
  `Builder.Update`; the builder does not accept that struct directly. Maps remain
  appropriate when dynamic keys or a map contract are the subject.
- Distinguish JSON decoding tags from database mapping tags. State when decoding
  and validation already happened; do not imply that an update identifies or
  authorizes its target from arbitrary request fields. Keep the writable shape
  specific to the operation, with required filters or allowlists visible.
- Explain the observable result: a typed value, empty collection, missing-row
  error, affected-row count, or another actual outcome. Separate backend-specific
  behavior from portable guarantees.
- Keep error results visible. Omit routine propagation in focused fragments;
  retain branches that teach absence, recovery, authorization, ownership, or
  another consequential decision. Complete runnable programs handle all errors.

Use [Example Completeness](examples-and-reference.md#example-completeness) for
formatting, construction variations, resource handling, and verification rules.

## Page Summary

Every completed new or materially revised guide must have a `summary` frontmatter
list. Write it after the body and examples have settled. Unreviewed legacy pages
and unfinished placeholders can omit it until their turn; do not bulk-generate
summaries for content that has not been reviewed.

Each item is one short sentence describing actual contents and the reader's
payoff. Enumerate meaningful learning units, not every heading or API method.
Combine closely related details; use only as many items as the page needs. The
summary is descriptive prose, not another table of contents or a set of anchor
links. Every item must be supported by the finished body.

For example:

```yaml
summary:
  - Read single values or collections into Go types and handle missing rows.
  - Execute targeted writes using request structs and interpret the affected-row count.
  - Map SQL columns and nullable values to struct fields.
```

Author the list in the shared `.md`, never in a page-ID switch or a parallel
website-only content map. The shared website template renders it below the
subtitle and above the body, in a single column with rose monospace numbering
and readable text. Secondary metadata links, such as Markdown, belong at the
right of the title. Authors supply prose, not layout markup, styling, or numbers.
Do not recreate that presentation inside the Markdown body.

## Review and Delivery

Read the whole page in reader order, then verify technical fidelity separately.
For a rewrite, account for corrections, intentional removals, and relocated
material. A fresh page boundary is not a reason to lose a safety condition.

Complete the [mandatory guide review](revision-and-links.md#mandatory-guide-review).
Check links to moved pages and renamed headings, including incoming links from
other guides and blog posts. Run `make generate` and `make docs` after guide
changes. Verify complete programs by compiling them; API-check focused fragments
and report whether any were actually executed.

For a local preview, follow `website/AGENTS.md` for server management, and inspect
representative narrow and wide layouts. When the user requests a review server,
keep it available until the agreed approval point, then stop that server without
affecting unrelated servers.

Report the page URL, key editorial choices, verified examples and claims, checks
run, and remaining failures concisely. Treat generated HTML, passing tests, and
agent review as distinct evidence, not proof that the writing is good. During an
interactive rewrite, wait for page feedback before advancing to another page.
