# Prose Style

## Contents

- [Voice](#voice)
- [Attention and progress](#attention-and-progress)
- [Sentence patterns](#sentence-patterns)
- [Paragraph rhythm](#paragraph-rhythm)
- [Ambiguity and precision](#ambiguity-and-precision)
- [Terminology and emphasis](#terminology-and-emphasis)
- [Transitions and branches](#transitions-and-branches)
- [Before and after](#before-and-after)
- [Sources and adaptation](#sources-and-adaptation)

## Voice

Write as a capable engineer helping another capable engineer. Be warm without becoming informal, and confident without claiming more than the product proves.

- Address the reader as `you`; address the product by its actual name, a short noun, or `it`.
- Describe current behavior in the present tense: “The client stores the token in memory.”
- Put the reader's situation and goal before the API. For a new concept or a non-obvious choice, follow the use-case-first pattern below.
- Use active voice for action and responsibility: “The worker deletes completed jobs.” Use passive voice only when automatic behavior is the point: “The cache is cleared when the command runs.”
- State the capability directly: “The client retries failed requests” rather than “The client makes it easy to retry failed requests.” Reassurance should answer a real concern, not judge how easy the reader should find the task.
- Use friendly reassurance only to remove real uncertainty: “You do not need to register concrete classes manually.” Do not use empty encouragement.
- Give each sentence one main job. Keep a condition, reason, or consequence with its claim when separating them would make the relationship harder to follow.

## Attention and Progress

Earn attention through relevance, visible progress, and connected explanations.
Write for readers who may scan, arrive mid-page, or return after an interruption;
do not assume an age group or a fixed attention span.

- **Make the opening useful.** Name a recognizable problem and what the reader
  can accomplish or understand. Skip a capability inventory and promises about
  what the page will cover.
- **Deliver an early payoff.** After necessary context and safety conditions,
  show the first useful result, decision, or mental model. A concept page can
  clarify a distinction before showing code; a reference can answer a lookup
  immediately. Do not move required setup or a warning later to reach code sooner.
- **Reveal complexity when it becomes relevant.** Establish the ordinary path,
  then explain its variations. Keep the reason for a choice beside the choice.
  Move unrelated depth to a linked section, not essential constraints.
- **Give scanning readers a route.** Use descriptive headings and informative
  paragraph openings. Keep names and units visible where readers look them up.
  A reader who enters at a subsection should find its assumed setup or a direct link.
- **Use variety for meaning.** Prose explains relationships; code demonstrates
  an action; lists separate independent items; tables support comparisons.
  Change format when the reader's job changes, not to decorate the page.

Engagement comes from understanding something useful, not from jokes, invented
stories, teaser headings, or forced questions. Retain a coherent scenario across
sections where it helps the reader see progress. References need predictable
entries, not a narrative imposed on every lookup.

## Sentence Patterns

Choose a pattern that tells the reader whether a statement is a fact, an action, a choice, or a requirement.

| Purpose | Pattern | Example |
| --- | --- | --- |
| Define | ``The `Thing` class provides …`` | ``The `Client` class provides a fluent interface for sending requests.`` |
| Start a task | ``To <goal>, <action>.`` | ``To create a report, call `reports.create`.`` |
| State default | ``By default, <behavior>.`` | ``By default, responses time out after 30 seconds.`` |
| State result | ``When <action>, <result>.`` | ``When the command completes, it writes the generated file to `config/`.`` |
| State a condition | ``If <condition>, <result>; otherwise, <result>.`` | ``If validation fails, the client throws an error; otherwise, it returns the parsed result.`` |
| State a reason | ``This is useful when <context>, since <cause>.`` | ``This is useful when requests are slow, since the worker can continue processing other jobs.`` |
| Introduce a choice | ``If you need <goal>, <alternative>.`` | ``If you need to preserve the input value, use `copy` instead.`` |
| Contrast | ``However, <limit>.`` | ``However, streaming responses cannot be retried after bytes have been sent.`` |
| Explain shorthand | ``In other words, <plain-language restatement>.`` | ``In other words, each request receives its own isolated context.`` |

Use modal verbs precisely:

- `can`: an ability or supported outcome — “The parser can read YAML files.”
- `may`: permission or possibility; make the intended meaning explicit, using `might` when permission could be confused with uncertainty.
- `might` or `may have`: a possible outcome or uncertain past event — “The broker may have accepted the message.”
- `should`: a recommendation with a reason — “You should use a dedicated cache in production.”
- `must`: a non-negotiable precondition or invariant — “The key must be unique.”
- `will`: deterministic behavior — “The command will overwrite the generated file.”
- `typically` or `usually`: a convention, never a guarantee — “Generated handlers are typically stored in `src/handlers`.”

Remove filler such as “simply”, “just”, “obviously”, and “clearly”. Preserve
meaningful uncertainty: “may have accepted” is not interchangeable with
“accepted”, and a recommendation is not a requirement.

## Paragraph Rhythm

**Use-case first:** give the reader a reason to care before explaining a
mechanism. Start with a concrete situation, name the problem or desired outcome,
then introduce the concept and API that address it. For example:

> A payment notification may need to send a receipt, but the provider should not
> have to wait for your mail service. Put the verified event on a queue so a
> worker can send the receipt later. Use `Dispatch` to make that handoff.

“Use `Dispatch` for slow work” skips the explanation: the reader still has to
guess who is waiting, what moves to the worker, and why the choice helps. Explain
those relationships in ordinary language before discussing acknowledgement,
payload conversion, or compatibility. Introduce a term such as “data transfer
object” only when the reader understands the problem it solves.

Add this context when a section introduces an unfamiliar concept, changes the
approach, or asks the reader to choose a policy. Reuse the page's existing
scenario rather than inventing a story for every subsection. A direct lookup
entry or an obvious next step can stay brief. Being concise means removing
distractions, not removing the explanation that makes the code meaningful.

Use short paragraphs with a clear job. A reliable teaching beat is:

1. **Orient:** describe the reader's situation and why the next step is useful.
2. **Act:** show the smallest useful command or call.
3. **Interpret:** explain the result, default, or automatic work.
4. **Branch:** introduce the next common variation only after the baseline is clear.

An opening paragraph often needs only two or three sentences. Treat that as a
starting point, not a limit. For a new concept, use situation → purpose → mechanism:

````markdown
A report request can fail because its server is briefly unavailable. A retry
policy gives that request another chance without making your application repeat
the call itself. Configure a retry limit on the client:

```ts
const client = createClient({ retries: 2 });
```

The client retries a failed request at most two times before returning the final
error to the caller.
````

Use one-sentence paragraphs for a consequential default, a transition into code, or an exception that readers must not miss. Do not use them repeatedly for ordinary prose.

Keep cause and effect adjacent. Prefer “The worker is long-lived, so restart it after deployment” to two distant paragraphs that make the reader reconstruct the dependency.

Build each paragraph around one reader question. Start with its point, then
develop the explanation with a mechanism, example, consequence, or useful
contrast. Connect new information to something already established. A sequence
of short facts is not an explanation if the reader must supply the links.

Vary sentence length with purpose: a short sentence can emphasize a boundary;
a longer sentence can keep a cause and its consequence together. Split nested
conditions or unrelated claims, not every sentence above an arbitrary word count.
Use paragraph breaks when the reader's question changes, rather than after every
sentence. Keep narrative reasoning in prose and independent constraints in lists.

Before cutting a clause, ask whether it supplies a reason, constraint, or
distinction that prevents a likely misreading. If it does, keep the information
and improve its placement. Remove repeated explanations and syntax narration,
not the reasoning that makes a page worth reading.

## Ambiguity and Precision

Use controlled-language discipline where a wrong reading changes an action,
scope, or guarantee. This is targeted ambiguity review, not STE certification
or a restricted vocabulary for the whole site.

- Name who acts. Repeat a component's name when `it`, `this`, or `they` could
  refer to more than one thing. Keep technical names stable instead of rotating synonyms.
- Put a condition before an instruction that depends on it. Separate the
  prerequisites from the action itself: a list headed “Before cleanup” must not
  include the cleanup command as one of those prerequisites.
- Place `only`, `not`, and bounds next to what they qualify. State which items
  an `and` or `or` joins when the grouping changes behavior. Break up long noun
  strings while preserving established technical terms.
- Keep a shared condition attached to every branch it controls. If the source
  means `A and (B or C)`, do not recast it as `(A and B) or C`. Separate the
  shared prerequisite from the alternatives when prose grouping is unclear.
- Keep units, inclusivity, initial versus additional attempts, and per-request
  versus shared limits explicit. Preserve requirement strength and honest uncertainty.
- Describe a mechanism's purpose without promising its outcome. “Coordinates
  shutdown to help avoid lost work” does not mean “prevents lost work”; caller
  responsibilities and cooperative cancellation still matter.
- If the source permits multiple readings, request the missing fact. Mark the
  passage as unresolved rather than leaving ambiguous instructions as publishable
  prose with a question hidden in a change note.

## Terminology and Emphasis

Define a specialized term on first meaningful use, then use it consistently.

```markdown
A **connection** identifies a backend service. A **queue** is a named stream of
work within that connection.
```

- Use inline code for identifiers readers must type or recognize: commands, filenames, environment variables, types, methods, option names, paths, and literal values.
- Use bold only for a newly defined term or a consequential qualifier. Do not bold ordinary instructions.
- Use quotation marks only when discussing a literal label as language, not for product terms that have a code form.
- Distinguish neighboring terms explicitly: request versus response, connection versus channel, authentication versus authorization, configuration versus runtime state.
- Name conventions before customization: default directory, inferred name, default timeout, default encoding, or generated identifier.

## Transitions and Branches

Use transitions to show the relationship between ideas, not to decorate paragraphs.

| Transition | Use it for |
| --- | --- |
| `For example,` | introduce a representative scenario or snippet |
| `In addition,` | add a related capability without changing the path |
| `Similarly,` / `Likewise,` | apply an established pattern to a sibling case |
| `Alternatively,` | offer a valid different implementation path |
| `However,` | introduce a material exception or limit |
| `For this reason,` | state the action implied by the preceding cause |
| `Before getting started,` | establish a prerequisite or distinction |

Use `First`, `Next`, and `Now` only in a short, linear walkthrough. Stop numbering once the reader starts choosing among alternatives.

Make branches explicit. Do not merge mutually exclusive paths into a paragraph:

````markdown
By default, the command uses the local destination. If you need to publish to a
named destination, pass `--destination`:

```shell
tool publish events.created --destination=critical
```
````

## Before and After

| Weak | Prefer |
| --- | --- |
| “This is a powerful feature.” | “This feature lets you defer work until a worker is available.” |
| “Configure the client as needed.” | “Set `timeout` to the maximum duration for one request attempt.” |
| “Errors are handled automatically.” | “Network failures are retried twice; other errors are returned immediately.” |
| “Here is an example.” | “To return a JSON response, pass an object to `respond.json`.” |
| “You can also use X.” | “If requests must share a connection, use X instead; it serializes access.” |
| “Be careful in production.” | “Do not enable debug output in production; it can expose request data and configuration values.” |

These adaptations use the pilot guides' existing subjects; verify their product
claims against current source whenever the examples are reused.

| Subject | Harder to read | Prefer and why |
| --- | --- | --- |
| Application lifecycle | A general account of startup, running, and shutdown before the reader knows what to register. | “Inside a composer, register initialization with `Startup`, supervised loops with `Run`, and acquired resource release with `Cleanup`.” Follow with composition-before-startup ordering: readers get a useful map before the details. |
| Producing jobs | One paragraph combining the job definition, wire message, worker policy, and deployment implications. | Distinguish the definition from the dispatched message first; explain the consumer's policy afterward. Keep the relationship, but let each paragraph answer one question. |
| Responses | “First … Second …” joining deadline renewal, blocked producers, and shutdown behavior. | Separate write-timeout behavior from producer cancellation. The reader can find each responsibility without reconstructing two different failure paths. |

## Sources and Adaptation

This guidance is expressed for Arkoren rather than importing another skill's
house style. Useful sources include [Google's paragraph guidance](https://developers.google.com/style/paragraph-structure)
and [voice guidance](https://developers.google.com/style/tone),
[ISO's overview of plain language](https://www.iso.org/standard/78907.html),
[ASD's STE explanation and scope](https://www.asd-ste100.org/STE_faq.html),
[Clarity](https://github.com/addyosmani/clarity), and
[Technical Writing Density](https://github.com/mlwelles/technical-writing-density).
Project contracts and terminology take precedence. These techniques do not
establish ISO or STE conformance, and an agent review does not establish human
comprehension.
