---
name: developer-documentation
description: Write and revise documentation for people (guides, READMEs, tutorials, and troubleshooting). Use when writing human-facing Markdown in cmd/arkoren/skills/arkoren/references/, website/, or a README. Not for cmd/arkoren/skills/arkoren/SKILL.md or `.agents/skills/` skill files, which are agent-only.
---

# Developer Documentation

Write task-first documentation that makes the safe, ordinary path easy to find and the product's behavior easy to trust. State observable results, defaults, constraints, and failure behavior; do not make readers infer them from code.

## Scope

Blog posts are the website-only exception: write them in
`website/src/content/blog/`, not the shared documentation tree. Before adding or
editing a post, read the **Publish a blog post** section in
`website/README.md` for frontmatter, permanent URLs, drafts, and category rules.
Keep release claims verifiable; a sample post must not invent a product release.
For a post's optional reader's margin, follow the frontmatter examples in that
README. Keep the author's desk, behind-the-post note, and reading shelf
supplementary; essential information belongs in the article, since the margin
is hidden on smaller screens. Use verified details rather than invented anecdotes.
Docs and blog layouts share `website/src/components/docs/Outline.astro`;
keep responsive section navigation there, separate from editorial margin content.
For website routing or deployment documentation, read `website/README.md` for
the static error-document setup; generated documentation uses the same fallback.

The guides are written once in `cmd/arkoren/skills/arkoren/references/guides/`
(one level of groups; see [Arkoren guides](references/guides.md)). The CLI embeds
that tree directly and the website renders it and generates llms.txt files from
it. Both readers use the same plain GFM text. Internal guide links use relative
`.md` paths, optionally with fragments; same-page fragments and external HTTPS
sources are allowed. API declarations and examples belong in Go doc comments;
link their canonical pkg.go.dev package pages. The website API catalog lists
modules, not copied API pages. `cmd/arkoren/skills/arkoren/SKILL.md` is agent-only
and follows `writing-for-agents`. `make generate` refreshes only its guide index;
author page contents and frontmatter directly. Generation requires installed
website Node dependencies; the website build checks freshness without writing.
Review offline source-link containment and editorial source conventions manually;
the custom raw-Markdown policy suite is retired. No archive-generation step exists.

## Contents

- [Scope](#scope)
- [Workflow](#workflow)
- [Writing settings](#writing-settings)
- [Reference map](#reference-map)
- [Non-negotiables](#non-negotiables)
- [Final pass](#final-pass)

## Workflow

1. Establish the actual contract from current source and tests, using existing documentation and examples as leads rather than proof. Check example calls and names against the API and repository conventions. Record exact names, scopes, defaults, prerequisites, outputs, failures, and version boundaries. Do not invent behavior.
2. Resolve the [writing settings](#writing-settings) from the request before choosing a page shape. For prose changes, load [Prose style](references/prose-style.md); for a new or materially revised guide, also load the [mandatory guide review](references/revision-and-links.md#mandatory-guide-review) before outlining. Load other references when the task needs them.
3. Establish the reader's starting situation and intended outcome before outlining. Order pages and sections by the questions, actions, and decisions needed to reach that outcome, not by an API inventory. Each section must advance that path or serve an identified lookup need. Group related setup, constraints, examples, and results; introduce alternatives when there is a reason to choose them. Resolve this organization before polishing sentences.
4. Write examples as focused teaching units: reader's situation and goal → action → minimal snippet → observable behavior. Load [Example completeness](references/examples-and-reference.md#example-completeness) before changing code blocks: establish the smallest default construction first, then teach optional settings separately. When the purpose is not obvious, follow the [use-case-first pattern](references/prose-style.md). Name existing setup in prose and link to its own example. Reserve end-to-end code for an explicit quickstart or integration lesson.
5. Place each safety, production, lifecycle, security, compatibility, and destructive-operation constraint beside the action it qualifies. State a constraint before the example when acting first could cause harm.
6. Review reader direction before style approval, using [Revision and links](references/revision-and-links.md). Follow incoming links and the page sequence, then read each whole page. Explain what each section enables the reader to understand, do, or decide next and why it belongs there; repair disconnected facts, unexplained alternatives, and unnecessary page jumps. Review fidelity separately against the verified contract and update affected links and examples.

## Writing settings

These are task-level writing controls, not page frontmatter or build configuration.
Apply the defaults unless the user requests an override in ordinary language.
Do not ask the user to configure them for routine work.

| Setting | Default | Override |
| --- | --- | --- |
| `page_type` | Package usage: when to use the capability, how to use it, and consequential behavior. | An explicitly requested quickstart, tutorial, integration lesson, runbook, or reference; preserve that purpose when revising an existing page of that type. |
| `detail` | Concise: one reader goal per section, without unrelated setup or repeated sibling-page explanations. | Requested deeper coverage of named topics. |
| `examples` | Small, concise teaching units tailored to the immediate need; no fixed line maximum. | Honor an explicitly requested size or complete-example scope. |
| `authored_contents` | Omit hand-written contents lists, tables of contents, and in-page indexes. The website supplies section navigation. | Include only when the user explicitly asks for one. |

A request to document a package or add subpages does not request an app-building
tutorial. Assume an existing project and name required collaborators in prose.
Showing the documented package's own constructor is focused usage, not a tutorial.
Keep project creation, installation, `main`, signal handling, and complete
application wiring in an explicitly requested onboarding or integration lesson.
Explain why an operation is useful, show its focused use, then state the result.

Include only code needed to understand or adapt the operation safely. Omit
unrelated setup and routine error checks that only return, wrap, log, or panic;
keep the error result visible. Retain checks that determine ownership, recovery,
authorization, or another consequential decision. Complete implementations still
handle their errors. Do not compress formatting or split examples arbitrarily
to make them look small; link complete setup when it belongs elsewhere.
Shared collaborators can connect a package explanation without turning it into
a cumulative app-building tutorial. Group `index.md` metadata and generated
guide indexes are navigation infrastructure, not authored in-page contents lists.

## Reference Map

| Need | Read |
| --- | --- |
| Write or place an Arkoren guide: the tree, files, page shape, mandatory driver compatibility matrices, per-driver structure and companion capabilities, depth | [Arkoren guides](references/guides.md) |
| Make prose readable: relevance, early payoff, connected explanations, ambiguity, terminology, and sentence rhythm | [Prose style](references/prose-style.md) |
| Choose a page shape for onboarding, concepts, tasks, runbooks, catalogs, or comparisons | [Page patterns](references/page-patterns.md) |
| Write code blocks, walkthroughs, method entries, tables, output, and test examples | [Examples and reference entries](references/examples-and-reference.md) |
| Explain contracts, defaults, errors, notes, warnings, security, production behavior, and troubleshooting | [Contracts and caveats](references/contracts-and-caveats.md) |
| Document sentinel values, laziness, mutation, resource ownership, ordering, scope, precedence, and integration boundaries | [Behavioral details](references/behavioral-details.md) |
| Revise a page, repair common weaknesses, and place links consistently | [Revision and links](references/revision-and-links.md) |

Read the relevant reference files directly; each contains a table of contents, exact patterns, and framework-neutral examples. Keep detail in the references rather than duplicating it here.

## Non-negotiables

- Write guides for consumers using Arkoren in their own applications. Follow
  the [consumer audience rules](references/guides.md#consumer-audience); keep
  toolkit-contributor instructions in explicitly labeled contributor documentation.
- Write consumer installation as released software: use canonical `go get
  arkoren.dev/core`, `go get arkoren.dev/driver/<vendor>`, `go install
  arkoren.dev/cmd/arkoren@latest`, or `go get -tool
  arkoren.dev/cmd/arkoren@latest`. Keep contributor checkout/build workflows
  separate. Consumer guides, README templates, and website copy use
  these commands without local replacements or prerelease fences. Never invent
  a version tag or claim that a download was verified without testing it.
- Address the reader directly, use present-tense product behavior, and state conditions with explicit `if`/`otherwise` language.
- Define a new term before using it as an instruction. Use its exact name consistently afterward.
- Start with the recommended, safe default. Label alternatives with their decision criterion and trade-off.
- Give each code block a purpose and an interpretation. Include imports, setup, paths, types, or output whenever omission would prevent adaptation.
- State success, failure, automatic behavior, mutation, ownership, scope, ordering, timing, and limits when they affect implementation.
- Use notes for material clarifications and warnings for concrete harm. A warning names the risk, cause, and safe action.
- Link a descriptive concept at the point it becomes necessary. Prefer the canonical entry instead of duplicating alias documentation.
- Prefer concrete outcomes over praise. Never hide a prerequisite, default, incompatibility, security boundary, or destructive consequence.
- Earn attention through relevance, visible progress, and connected explanations. Support scanning without making careful reading unrewarding; follow [Prose style](references/prose-style.md#attention-and-progress).

## Final Pass

For website test changes, follow `website/AGENTS.md`: protect reader journeys
and publishing contracts, not the current copy or decorative implementation.

Before delivering documentation, load [Revision and links](references/revision-and-links.md). For a new or materially revised guide, complete its mandatory guide review, validation commands, and review report. For other human-facing documentation, complete its page-ready checklist. Report checks not run and claims not verified; neither counts as a pass.
