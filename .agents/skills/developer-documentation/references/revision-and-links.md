# Revision, Links, and Final Review

## Contents

- [Review in reader order](#review-in-reader-order)
- [Compare meaning after editing](#compare-meaning-after-editing)
- [Linking strategy](#linking-strategy)
- [Consistency checks](#consistency-checks)
- [Common repairs](#common-repairs)
- [Mandatory guide review](#mandatory-guide-review)
- [Validation](#validation)
- [Page-ready checklist](#page-ready-checklist)

## Review in Reader Order

Review comprehension first, then compare meaning in a separate fidelity pass.
These passes answer different questions: useful prose can be inaccurate, and
accurate prose can still require unnecessary effort.

Reader direction is mandatory before style approval. For a group of pages,
follow its incoming links and sidebar order: check what the reader already knows,
the first useful payoff, each page's distinct purpose, and the reason to continue.
Group pages or sections when one ordinary task otherwise requires unnecessary
handoffs. Within a page, explain what each section lets the reader understand,
do, or decide next and why it belongs here. Repair disconnected facts, unexplained
alternatives, and separated prerequisites/results. Lookup pages need direct
answers, not a forced linear story.

1. Read the title and headings. Can a scanning reader locate the common task?
   Check authored contents only when the user requested them.
2. Read the introduction. Does it define the capability and practical result without a feature dump?
3. Follow the first path literally. Do prerequisites, imports, files, commands, and expected outputs appear before they are needed?
4. Inspect every branch. Does it state when to choose it and what changes compared with the default?
5. Inspect every warning. Is it adjacent to risk, concrete, and actionable?
6. Inspect each reference entry independently. Can a reader understand the usual call and contract without rereading the entire page?
7. Remove information that repeats the code without explaining behavior.
8. Read each section's opening without its code. Can the reader tell what problem it solves and why they would use it? If not, add the missing situation and purpose before the API details; follow [Prose style](prose-style.md#paragraph-rhythm). An obvious operation or direct lookup can remain brief when its purpose is already clear.
9. Read headings and paragraph openings as a scanning reader. Can you locate the
   answer and the next useful action without reading the entire page? Keep
   required context and safety conditions at the point of use.
10. Read the explanation continuously. Does each paragraph answer one question,
    connect to the preceding idea, and leave the reader ready for the next?
    Repair missing mechanisms before shortening sentences. See
    [Attention and progress](prose-style.md#attention-and-progress).

For a complete pass, inventory all maintained pages and record which were read;
keyword searches identify candidates but do not establish review coverage.

## Compare Meaning After Editing

Compare the original and revision against the verified product contract. If the
original was wrong, correct it from source and identify that correction separately
from stylistic changes. For each affected operation, account for:

- actor, action, condition, exception, negation, and the scope of `only`;
- numbers and their units, inclusive bounds, timing, and ordering;
- required versus recommended actions, permissions, and possible outcomes;
- identifiers, code, defaults, ownership, and failure behavior;
- causal explanations and distinctions whose removal could mislead a reader.

Account for every verified original claim without preserving its placement
automatically. Relocate unrelated depth to its canonical section or reference
and link where needed; keep essential conditions at the point of use. Identify
corrections and intentional removals explicitly, then identify revised claims
with no source. An equivalent imperative can preserve a requirement; matching
modal-word counts cannot prove equivalent meaning.
Mechanical checks are useful prompts, not approval gates: they can miss changed
units or a condition moved to a different action and can flag harmless rewrites.

When intended behavior is unknown, record the clarification and quarantine the
affected passage. A change note does not make unresolved instructions safe to
publish. Keep a useful, accurate passage unchanged when editing cannot improve it.

For a writing-rule change, compare old and revised guidance on real pages of
different types, preserving a baseline in the scratchpad. Use at least one
untouched page to check transfer beyond the examples used to design the rules.
Judge factual preservation separately from presentation and show representative
alternatives to the user. Record agent-only review as editorial evidence, not
proof of human comprehension; do not use word count or a reading-grade target
as a substitute for a reader completing or explaining the task.

Use the maintained [evaluation prompts](../evals/evals.json) and
[controlled fixtures](../evals/fixtures.md) for writing-rule regressions. Resolve
the `files` paths relative to this skill's directory, not the `evals/` directory.
Refresh real-page selections as the docs evolve; tests protect meaning and reader tasks,
not exact published wording. Keep trial outputs and review reports in the
scratchpad, not in this skill.

## Linking Strategy

Link a concept at its first useful mention, not only in a final “See also” list. Use the linked noun or action as the anchor text.

```markdown
Configure [request authentication](../security/authentication.md) before sending
requests to a protected endpoint.
```

Use links for:

- a prerequisite the current page deliberately does not teach;
- a related feature needed to finish the current task;
- a detailed reference that would interrupt the current explanation;
- an external protocol, dependency, or platform source of truth.

Use an internal section link when the reader should continue immediately. Use a separate-page link when following it requires a different mental model or setup path. Do not use naked URLs, generic “here” links, or link every repeated noun.

When you name an alias or alternative, point to the canonical entry instead of duplicating its contract.

## Consistency Checks

Check that terms, code, tables, and prose agree:

- The title, heading, filename, command, type, option spelling, and anchor use one canonical name.
- The documented default matches the example's omitted option behavior.
- The table's units, bounds, and inclusivity match the prose.
- An error example matches the documented exception, status, or return type.
- Every cross-reference resolves to an existing heading or page.
- Every snippet uses the same fictional domain and identifiers unless it begins a new scenario.
- The page uses the project's established code fences, GitHub alerts, and link style. Shared documentation uses portable GFM rather than website-only tabs or components.

Use US English in handwritten documentation. Preserve API identifiers, official
names, quotations, and literal output. Apply spelling fixes to pages being
revised rather than rewriting the corpus solely for spelling.

## Common Repairs

| Symptom | Repair |
| --- | --- |
| A page begins with code but no purpose. | Add a two-sentence orientation and label the reader goal before the first sample. |
| A section starts with a method or jargon before its purpose is clear. | Explain the practical situation and desired outcome first, then introduce the mechanism. |
| A quickstart has setup but no success signal. | Add the exact URL, output, state, or assertion that confirms success. |
| A warning says “be careful”. | Replace it with the risky action, consequence, and mitigation. |
| A method entry repeats syntax. | Add return, mutation, default, failure, or performance semantics. |
| Several variants are listed without guidance. | Introduce the default, name the selection criterion, and compare the trade-off. |
| A table is hard to interpret. | Add units, scope, defaults, and inclusion rules; move narrative decisions back into prose. |
| A complete example cannot run. | Add the missing setup or correct the example. Focused fragments instead name existing collaborators in prose; they do not need a complete application harness. |
| A link interrupts a sentence. | Keep the reader on the page until the current action is complete, then link the next needed concept. |

## Mandatory guide review

Apply this gate to each new or materially revised content page under
`cmd/arkoren/skills/arkoren/references/guides/`. A material revision changes instructions,
examples, behavior claims, or organization. A spelling or punctuation correction
does not require a new full-page review. Automated checks still cover all guides.
Group indexes are metadata, not articles. READMEs, blogs, Go doc comments and
agent instructions retain their own page rules. API declarations are not
runnable examples.

### Review procedure

1. Identify the changed pages' reader tasks and page types using the table below.
   A mixed page must satisfy the requirements of its teaching tasks.
2. Review each whole page in reader order for comprehension, then perform the
   [fidelity pass](#compare-meaning-after-editing) against every rule below. Use the
   linked references for details. For drivers pages and embedded Drivers
   sections, check each provider's relevant shipped capabilities and compatibility
   matrix against source/tests. Fix failures and repeat affected checks.
3. Verify complete examples by compiling them, fragments against current APIs,
   and behavior claims against source and tests. Run the validation commands.
4. Report changed pages, verification evidence, unresolved failures and checks
   not run in the change summary or PR. No separate report file or per-rule
   status ledger is required. Missing evidence remains unverified, not a pass;
   do not average safety or fidelity failures into a quality score.

### Page types

| Type | Required emphasis |
| --- | --- |
| Quickstart or tutorial | Complete cumulative path, exact file/edit locations, handled errors, visible success checkpoint, cleanup when resources persist. |
| Task guide | Named starting state, prerequisites, focused action, observable result, relevant failure handling. |
| Concept guide | Practical mental model, defined terms, distinctions, consequences, and trade-offs; installation only when needed. |
| Operations or runbook | Preconditions, safe ordering, repeatability, recovery, and verification of the final state. |
| Driver comparison | Selection criteria first; each provider follows the [per-driver structure](guides.md#per-driver-structure), including task examples and shipped companion capabilities. |
| Catalog or reference | Stable repeated entries, shared setup or notation, concise contracts and examples, explicit exceptions; no forced narrative journey. |

Use [Page patterns](page-patterns.md) for detailed shapes. Operational and
security pages may establish the risk model before showing the ordinary action.
An installation page may lead with prerequisites. A catalog may establish
notation before its entries and omit an `Introduction` heading.

### Rules

| ID | Passing condition | Check |
| --- | --- | --- |
| G01 Identity | Accurate, nonempty metadata; one descriptive H1 matching the title; useful navigation label. | Metadata validation, rendered title checks and editorial review. |
| G02 Orientation | Opening establishes the reader's goal, necessary definitions, and scope without a feature dump. | [Prose style](prose-style.md#paragraph-rhythm). |
| G03 Prerequisites | Tools, permissions, resources, variables, and existing setup are established before use; linked setup is sufficient. No literal `Prerequisites` heading is required. | Reader-order review. |
| G04 Order | Choice criteria precede setup; the safe recommended path precedes alternatives and exhaustive maps, except when the risk model or map is prerequisite. | [Page patterns](page-patterns.md). |
| G05 Navigation | Title Case titles/labels; sentence-case internal headings preserving names and identifiers; parallel siblings, valid hierarchy through H4. | Editorial review against [guide conventions](guides.md#titles-and-sidebar-labels). |
| G06 Examples | Purpose, assumed setup, and result are clear in context; complete examples compile and handle errors; focused fragments retain consequential checks. | [Examples](examples-and-reference.md#example-completeness) and verification evidence. |
| G07 Behavior | Relevant defaults, failures, ownership, mutation, timing, ordering, and limits are explicit and agree with current source/tests. | [Behavioral details](behavioral-details.md). |
| G08 Safety | Essential risk, cause, and safe action appear before or beside the first consequential use; longer explanations may follow. | [Contracts and caveats](contracts-and-caveats.md#notes-and-warnings). |
| G09 Links | Descriptive links appear where needed; internal relative `.md` targets and fragments resolve; authoritative external HTTPS sources are allowed. | Rendered checks plus manual offline source-link and editorial review. |
| G10 Completion | Task readers can recognize success; package guides end with the relevant API. Non-package pages link a next task when useful, without a forced API link or unrelated ending. | Reader-order review. |
| G11 Driver coverage | Every provider on a drivers page or in a Drivers section follows the [per-driver structure](guides.md#per-driver-structure); its relevant shipped capabilities have setup, task examples, constraints, and reference links. The mandatory [compatibility matrix](guides.md#compatibility-matrices) covers every provider and matches current implementation/tests and prose after every page or behavior change. Omit inapplicable optional sections rather than adding empty headings. | Provider-by-provider editorial review against vendor packages, examples and guides, plus rendered matrix checks. Applies when the page has driver sections. |
| G12 Consumer relevance | Every section serves someone using Arkoren in their own application, following the [consumer audience rules](guides.md#consumer-audience). Published imports and application paths are relevant; toolkit source layout, internal dependencies, and maintenance instructions belong in contributor documentation. | Section-by-section editorial review. N/A only for explicitly identified toolkit-contributor documentation. |
| G13 Reading paths | Before style approval, identify what each section enables the reader to understand, do, or decide next and why it belongs here. Incoming links, page order, and assumed knowledge form a coherent path. Fail disconnected facts, unexplained alternatives, separated prerequisites/results, and reference inventories that delay the ordinary path. Lookup pages retain direct answers rather than a forced story. | [Reader-order review](#review-in-reader-order), including the cross-page journey, headings-only, and continuous-prose passes. Repair organization before sentence-level polish; length alone is not evidence. |
| G14 Fidelity | Editing preserves verified claims, units, scope, conditions, obligations, uncertainty, and technical tokens; source-backed corrections are identified separately. Unknown intent is flagged and excluded from publishable instructions. | [Original/revision claim comparison](#compare-meaning-after-editing) plus source/API checks. N/A only when the page has no prior version; source grounding still applies under G07. |

These rules standardize reader orientation and safety, not identical headings
or a page word quota. Check the skill's writing settings separately: package
usage is the default, code examples are focused on the immediate need, and authored
contents require an explicit request. These checks do not replace comprehension
or fidelity review. Context may explain several related snippets without a
new paragraph after each fence. Use tables for comparison/lookup, not narrative
instructions; format identifiers and literal values as code.

## Validation

Run from the repository root after shared documentation changes:

```sh
make generate
make docs
```

Install website dependencies before generation and Playwright Chromium before
the full docs run. The build checks index freshness without writing and validates
metadata; rendered tests check links, fragments, IDs and navigation. Custom
raw-Markdown policy and offline source-link checks are retired. Review those
source conventions manually, since rendering may normalize source. None of
these checks judges prose quality or proves examples correct. Consult the actual
website tests for their coverage rather than assuming a successful build checks
every source convention. `make docs` also runs website functionality tests.
CI runs rendered documentation checks on every routine run.

Compile complete examples in a temporary module with local replacements as
described in [Arkoren guides](guides.md#code-examples). Record which programs
were compiled, which fragments were API-checked, and which commands were run.
Never describe an API-checked fragment as an executed program. For presentation
changes, inspect the rendered page at narrow and wide widths and report the
result or the missing visual verification.

## Page-Ready Checklist

- [ ] The page has one primary reader goal.
- [ ] The page follows the resolved writing settings: package usage unless another purpose was requested, small focused examples without unrelated code or unnecessary error checks, and authored contents only when explicitly requested.
- [ ] The introduction defines the capability, problem, and main entry point.
- [ ] The default path is complete, safe, and verified before alternatives appear.
- [ ] Each snippet's purpose and behavior are clear from its surrounding context.
- [ ] Defaults, conventions, automatic behavior, output, and failures are explicit.
- [ ] Prerequisites appear before dependent actions.
- [ ] Tables support comparison or lookup and define their units and scope.
- [ ] Notes clarify; warnings prevent concrete harm.
- [ ] Production, security, lifecycle, ordering, and destructive consequences are adjacent to the action that creates them.
- [ ] Links use descriptive text and point to the next necessary detail.
- [ ] Example calls match current API signatures and repository conventions; names, paths, types, values, and versions are internally consistent.
- [ ] The page explains decisions and consequences rather than narrating obvious syntax.
- [ ] Both scanning and continuous reading work for the page's intended job; useful progress is not delayed by unrelated detail.
- [ ] A separate original/revision comparison preserves meaning and accounts for source-backed corrections; unresolved instructions are not published.
