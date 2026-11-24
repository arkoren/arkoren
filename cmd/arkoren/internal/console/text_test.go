package console_test

import (
	"strings"
	"testing"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestTextAlignsLabelsAndKeepsOrder translates a flat object into aligned
// sentence-case labels in document order, with exact scalar text.
func TestTextAlignsLabelsAndKeepsOrder(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"zeta":1,"last_seen":"now","big":12345678901234567890,"ok":true,"tags":["a","b"]}`), false))

	want := `Zeta       1
Last seen  now
Big        12345678901234567890
Ok         true
Tags       a, b`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextOmitsEmptyValues drops null, empty strings, empty containers and
// containers that only held empties, but keeps false and zero.
func TestTextOmitsEmptyValues(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"a":null,"b":"","c":[],"d":{},"e":{"f":null},"g":false,"h":0}`), false))

	want := `G  false
H  0`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsNothingForAllEmptyDocuments leaves no blank output.
func TestTextPrintsNothingForAllEmptyDocuments(t *testing.T) {
	t.Parallel()

	if got := console.Text([]byte(`{"a":null,"b":[]}`), false); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

// TestTextJoinsShortListsOnOneLine keeps up to four space-free items that fit.
func TestTextJoinsShortListsOnOneLine(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"tags":["a","b","c","d"]}`), false))

	if got != "Tags  a, b, c, d" {
		t.Fatalf("got %q", got)
	}
}

// TestTextBulletsLongLists prints more than four items as a block.
func TestTextBulletsLongLists(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"tags":["a","b","c","d","e"]}`), false))

	want := `Tags
  - a
  - b
  - c
  - d
  - e`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextBulletsItemsWithSpaces prints sentences as a block even when few.
func TestTextBulletsItemsWithSpaces(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"effects":["one two","three"]}`), false))

	want := `Effects
  - one two
  - three`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextBulletsWideLists prints a list whose joined text exceeds 60 runes as
// a block.
func TestTextBulletsWideLists(t *testing.T) {
	t.Parallel()

	wide := strings.Repeat("x", 31)
	got := string(console.Text([]byte(`{"t":["`+wide+`","`+wide+`"]}`), false))

	if !strings.HasPrefix(got, "T\n  - ") {
		t.Fatalf("got %q", got)
	}
}

// TestTextFallsBackToBlocksWhenARowHoldsALongList turns a table into blocks.
func TestTextFallsBackToBlocksWhenARowHoldsALongList(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"items":[{"name":"a","tags":["1","2","3","4","5"]}]}`), false))

	if strings.Contains(got, "NAME") || !strings.Contains(got, "  - 5") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestTextWritesSummaryHeadline marks success and hides the envelope flags.
func TestTextWritesSummaryHeadline(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"summary":"Created project x","ok":true,"dry_run":false,"module":"x"}`), false))

	want := `✓ Created project x

Module  x`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextMarksFailedSummary uses a red cross when ok is false.
func TestTextMarksFailedSummary(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"summary":"2 layout violations","ok":false}`), true))

	if got != "\x1b[1m\x1b[31m✗ 2 layout violations\x1b[0m" {
		t.Fatalf("got %q", got)
	}
}

// TestTextMarksDryRunSummary uses a yellow arrow when dry_run is true.
func TestTextMarksDryRunSummary(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"summary":"Would add feature x","dry_run":true}`), true))

	if got != "\x1b[1m\x1b[33m→ Would add feature x\x1b[0m" {
		t.Fatalf("got %q", got)
	}
}

// TestTextColorsSuccessHeadline uses bold green for a plain summary.
func TestTextColorsSuccessHeadline(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"summary":"Done"}`), true))

	if got != "\x1b[1m\x1b[32m✓ Done\x1b[0m" {
		t.Fatalf("got %q", got)
	}
}

// TestTextWritesErrorHeadline prints the message in red, then the error's
// members.
func TestTextWritesErrorHeadline(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"error":{"message":"boom","type":"tool_error","status":500}}`), true))

	want := "\x1b[1m\x1b[31m✗ boom\x1b[0m\n\n" +
		console.ColorKey + "Type" + console.ColorReset + "    tool_error\n" +
		console.ColorKey + "Status" + console.ColorReset + "  " + console.ColorNumber + "500" + console.ColorReset

	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestTextErrorSharesNextStepsWithSuccesses puts the steps beside the error,
// not inside it, and prints them like a success's.
func TestTextErrorSharesNextStepsWithSuccesses(t *testing.T) {
	t.Parallel()

	raw := `{"error":{"message":"no inspector URL is configured","type":"not_configured"},"next_steps":[{"text":"Pass the endpoint with --url"},{"text":"See usage","command":"arkoren --help"}]}`
	got := string(console.Text([]byte(raw), false))

	want := `✗ no inspector URL is configured

Type  not_configured

Next steps
  1. Pass the endpoint with --url
  2. See usage  arkoren --help`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextWritesWarnings prints each warning as a marked line with its details
// as bullets, between the changes and the next steps.
func TestTextWritesWarnings(t *testing.T) {
	t.Parallel()

	raw := `{"summary":"Generated root certificate","changes":[{"action":"create","path":"root.pem"}],` +
		`"warnings":[{"message":"System trust was not changed","details":["It does one thing","It does another"]}],` +
		`"next_steps":[{"text":"Trust the root","command":"arkoren certificate root trust root.pem"}]}`
	got := string(console.Text([]byte(raw), false))

	want := `✓ Generated root certificate

Changes
  create  root.pem

! System trust was not changed
  - It does one thing
  - It does another

Next steps
  1. Trust the root  arkoren certificate root trust root.pem`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextWarningsAloneNeedNoHeadline prints the stderr trust document as
// marked lines, without a heading.
func TestTextWarningsAloneNeedNoHeadline(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"warnings":[{"message":"careful","details":["Action: trust"]}]}`), false))

	if got != "! careful\n  - Action: trust" {
		t.Fatalf("got:\n%s", got)
	}
}

// TestTextColorsStepCommands paints a step's command, not its text.
func TestTextColorsStepCommands(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"next_steps":[{"text":"Enter the project","command":"cd app"}]}`), true))

	if !strings.Contains(got, "1. Enter the project  "+console.ColorKey+"cd app"+console.ColorReset) {
		t.Fatalf("got %q", got)
	}
}

// TestTextShortensDigestsAndPaths prints 12 characters of a 64-character
// digest and paths under the working directory relative to it, but leaves
// commands exact.
func TestTextShortensDigestsAndPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	digest := strings.Repeat("ab12", 16)
	raw := `{"summary":"Created project in ` + dir + `/shop","sha256":"` + digest + `","directory":"` + dir + `",` +
		`"elsewhere":"/opt/key.pem","next_steps":[{"text":"Enter the project","command":"cd ` + dir + `/shop"}]}`
	got := string(console.Format([]byte(raw), console.Style{Text: true, Dir: dir}))

	for _, want := range []string{
		"✓ Created project in shop",
		"SHA256     ab12ab12ab12",
		"Directory  .",
		"Elsewhere  /opt/key.pem",
		"cd " + dir + "/shop",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}

	if strings.Contains(got, digest) {
		t.Fatalf("digest was not shortened:\n%s", got)
	}
}

// TestTextAlignsChanges prints actions, paths and dim details in columns.
func TestTextAlignsChanges(t *testing.T) {
	t.Parallel()

	raw := `{"summary":"Added","changes":[{"action":"create","path":"a.go"},{"action":"edit","path":"main.go","detail":"compose.Use(x)"},{"action":"tidy","path":"go.mod"}]}`
	got := string(console.Text([]byte(raw), false))

	want := `✓ Added

Changes
  create  a.go
  edit    main.go  compose.Use(x)
  tidy    go.mod`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextColorsChangeActions colors each action kind and dims the detail.
func TestTextColorsChangeActions(t *testing.T) {
	t.Parallel()

	raw := `{"changes":[{"action":"create","path":"a"},{"action":"edit","path":"b","detail":"d"},{"action":"remove","path":"c"},{"action":"write","path":"d"},{"action":"require","path":"e"}]}`
	got := string(console.Text([]byte(raw), true))

	for _, fragment := range []string{
		"\x1b[32mcreate" + console.ColorReset,
		"\x1b[33medit" + console.ColorReset,
		"\x1b[31mremove" + console.ColorReset,
		console.ColorKey + "write" + console.ColorReset,
		"\x1b[2mrequire" + console.ColorReset,
		"\x1b[2md" + console.ColorReset,
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("output lacks %q: %q", fragment, got)
		}
	}
}

// TestTextNumbersNextSteps prints a heading and numbered lines, aligning the
// commands of the steps that have one.
func TestTextNumbersNextSteps(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"summary":"Done","next_steps":[{"text":"Enter the project","command":"cd app"},{"text":"Start the application","command":"make run"},{"text":"Open the browser"}]}`), false))

	want := `✓ Done

Next steps
  1. Enter the project      cd app
  2. Start the application  make run
  3. Open the browser`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsFlatObjectListsAsTables uses uppercase headers in first-seen
// member order and leaves missing cells empty.
func TestTextPrintsFlatObjectListsAsTables(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"jobs":[{"name":"send-invoice","lastSeen":"10:02"},{"name":"reconcile","retries":3}]}`), false))

	want := `Jobs
  NAME          LAST SEEN  RETRIES
  send-invoice  10:02
  reconcile                3`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextNestsObjectsAsSections indents nested objects below a heading and
// aligns all one-line labels of an object in one column across sections.
func TestTextNestsObjectsAsSections(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"message":"failed","fields":{"/user_name":["required"]},"hint":"retry"}`), false))

	want := `Message  failed

Fields
  /user_name  required

Hint     retry`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextLeavesDataKeysUnchanged humanizes only identifier-like names.
// Paths and pointers are data, so they keep their exact spelling.
func TestTextLeavesDataKeysUnchanged(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"/a_b":1,"app.info":2,"user_id":3}`), false))

	want := `/a_b      1
app.info  2
User ID   3`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextIndentsMultilineValues aligns continuation lines under the value.
func TestTextIndentsMultilineValues(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"notes":"one\ntwo","id":1}`), false))

	want := "Notes  one\n       two\nID     1"

	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestTextEscapesControlCharacters keeps remote strings from emitting
// terminal escape sequences, in values, labels and table cells alike.
func TestTextEscapesControlCharacters(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"a\u001b":"x\u001b[31m","rows":[{"name":"\u0007"}]}`), false))

	if strings.ContainsAny(got, "\x1b\x07") || !strings.Contains(got, `x\u001b[31m`) || !strings.Contains(got, `\u0007`) {
		t.Fatalf("got %q", got)
	}
}

// TestTextColorsLabelsAndScalars colors labels, numbers and booleans.
func TestTextColorsLabelsAndScalars(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"n":1,"yes":true,"no":false,"s":"plain"}`), true))

	for _, fragment := range []string{
		console.ColorKey + "N" + console.ColorReset,
		console.ColorNumber + "1" + console.ColorReset,
		"\x1b[32mtrue" + console.ColorReset,
		"\x1b[31mfalse" + console.ColorReset,
		"  plain",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("output lacks %q: %q", fragment, got)
		}
	}
}

// TestTextPrintsScalarDocuments prints a top-level string without quotes.
func TestTextPrintsScalarDocuments(t *testing.T) {
	t.Parallel()

	if got := string(console.Text([]byte(`"hello"`), false)); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

// TestTextReturnsInvalidJSONUnchanged shows the user what was really sent.
func TestTextReturnsInvalidJSONUnchanged(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`{"a":`, `{} {}`, `nope`} {
		if got := string(console.Text([]byte(raw), false)); got != raw {
			t.Fatalf("Text(%q) = %q", raw, got)
		}
	}
}

// TestFormatSelectsTextOrJSON translates only when the style asks for text.
func TestFormatSelectsTextOrJSON(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"a":1}`)

	if got := string(console.Format(raw, console.Style{Text: true})); got != "A  1" {
		t.Fatalf("text = %q", got)
	}

	if got := string(console.Format(raw, console.Style{})); got != `{"a":1}` {
		t.Fatalf("json = %q", got)
	}
}

// TestTextPrintsListWrapperAsBareTable drops the Items heading and the resting
// truncated flag of a shipped list tool, here http.routes.
func TestTextPrintsListWrapperAsBareTable(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"method":"GET","pattern":"/","handler":"shop/web.Home","source":"web/home.go:12"},` +
		`{"method":"POST","pattern":"/orders/{id}","handler":"shop/web.(*Orders).Create","source":"web/orders.go:48"}],"truncated":false}`
	got := string(console.Text([]byte(raw), false))

	want := `METHOD  PATTERN       HANDLER                    SOURCE
GET     /             shop/web.Home              web/home.go:12
POST    /orders/{id}  shop/web.(*Orders).Create  web/orders.go:48`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextNotesTruncatedLists says more results exist, dim under the items.
func TestTextNotesTruncatedLists(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"items":[{"name":"a"}],"truncated":true}`), true))

	want := "\x1b[1mNAME\x1b[0m\na\n\n\x1b[2m… more results (narrow the query or use --verbose)\x1b[0m"

	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestTextHidesTheCursorOfFinishedLists keeps a resting cursor list quiet.
func TestTextHidesTheCursorOfFinishedLists(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"name":"a"}],"truncated":false,"cursor":"AbC123","dropped":0,"has_more":false,"instance_changed":false}`

	if got := string(console.Text([]byte(raw), false)); got != "NAME\na" {
		t.Fatalf("got %q", got)
	}
}

// TestTextPrintsNothingForEmptyLists prints no heading or flag for no records.
func TestTextPrintsNothingForEmptyLists(t *testing.T) {
	t.Parallel()

	if got := console.Text([]byte(`{"items":[],"truncated":false}`), false); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

// TestTextPrintsWideTablesAsBlocks falls back when the row would exceed the
// table width, even though every cell is short.
func TestTextPrintsWideTablesAsBlocks(t *testing.T) {
	t.Parallel()

	var members []string

	for _, name := range []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh"} {
		members = append(members, `"`+name+`":"`+strings.Repeat("x", 20)+`"`)
	}

	got := string(console.Text([]byte(`[{`+strings.Join(members, ",")+`}]`), false))

	if strings.Contains(got, "AA") || !strings.HasPrefix(got, "Aa  xxxxxxxxxxxxxxxxxxxx\nBb  ") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestTextKeepsLongLastColumnsInTables lets a long final description wrap
// instead of turning the whole list into blocks, as in tool discovery.
func TestTextKeepsLongLastColumnsInTables(t *testing.T) {
	t.Parallel()

	description := strings.Repeat("long ", 30)
	got := string(console.Text([]byte(`[{"command":"app:logs","kind":"read","description":"`+description+`"}]`), false))

	if !strings.HasPrefix(got, "COMMAND   KIND  DESCRIPTION\napp:logs  read  long long") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestTextCapitalizesAcronymsInLabels writes ID and URL the way people do.
func TestTextCapitalizesAcronymsInLabels(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"trace_id":"a","url_source":"env","http_status":200,"idle":1}`), false))

	want := `Trace ID     a
URL source   env
HTTP status  200
Idle         1`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// testNow is the clock of the goldens below: five seconds after 10:00:01.
var testNow = time.Date(2026, time.November, 23, 10, 0, 6, 0, time.UTC)

// at translates raw JSON with the fixed test clock and no color.
func at(raw string) string {
	return string(console.TextAt([]byte(raw), false, testNow))
}

// TestTextListsNestedArrays bullets one-line elements and prints nested
// elements as blocks.
func TestTextListsNestedArrays(t *testing.T) {
	t.Parallel()

	got := at(`["a",{"b":{"c":1}},2]`)

	want := `- a

B
  C  1

- 2`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextNamesTheNextCursor shows the continuation cursor of a cursor list only
// when more records exist, and keeps a nonzero dropped count visible.
func TestTextNamesTheNextCursor(t *testing.T) {
	t.Parallel()

	got := at(`{"items":[{"name":"a"}],"truncated":true,"cursor":"AbC123","dropped":4,"instance_changed":false}`)

	want := `NAME
a

Dropped  4

… more results (narrow the query, or continue with cursor AbC123)`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextReadsOlderCursorPages keeps pages from servers that still send
// has_more working: the flag is hidden and, when true, notes more results.
func TestTextReadsOlderCursorPages(t *testing.T) {
	t.Parallel()

	got := at(`{"items":[{"name":"a"}],"truncated":false,"cursor":"AbC123","dropped":0,"has_more":true,"instance_changed":false}`)

	want := `NAME
a

… more results (narrow the query, or continue with cursor AbC123)`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextKeepsHealthStatusAboveItsChecks puts the verdict before the check
// table of the health tool, without an Items heading.
func TestTextKeepsHealthStatusAboveItsChecks(t *testing.T) {
	t.Parallel()

	raw := `{"status":"failing","items":[` +
		`{"name":"database","status":"ok","since":"2026-11-23T09:00:00.5Z","checked_at":"2026-11-23T10:00:00.25Z"},` +
		`{"name":"cache","status":"failing","error":"connection refused","since":"2026-11-23T09:59:00Z","checked_at":"2026-11-23T10:00:00Z"}]}`

	want := `Status  failing

NAME      STATUS   SINCE                         CHECKED AT                    ERROR
database  ok       2026-11-23 09:00:00 (1h ago)  2026-11-23 10:00:00 (6s ago)
cache     failing  2026-11-23 09:59:00 (1m ago)  2026-11-23 10:00:00 (6s ago)  connection refused`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextShowsMillisAndBytesInReadableUnits prints "<name>_ms" and
// "<name>_bytes" numbers in human units under the label "<name>".
func TestTextShowsMillisAndBytesInReadableUnits(t *testing.T) {
	t.Parallel()

	raw := `{"duration_ms":1500,"fast_ms":12.5,"quick_ms":182,"tiny_ms":0.014,"runtime":{"heap_alloc_bytes":1258291,"sys_bytes":512,"num_gc":3}}`

	want := `Duration  1.5s
Fast      12.5ms
Quick     182ms
Tiny      14µs

Runtime
  Heap alloc  1.3 MB
  Sys         512 B
  Num gc      3`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextKeepsNumberTwinsWithTheirSuffix leaves a *_ms number alone when its
// base name is also a member, so no two lines get one label.
func TestTextKeepsNumberTwinsWithTheirSuffix(t *testing.T) {
	t.Parallel()

	if got := at(`{"duration":3,"duration_ms":1500}`); got != "Duration     3\nDuration ms  1500" {
		t.Fatalf("got %q", got)
	}
}

// TestTextDropsOlderServerTwins hides what an older server repeats beside the
// readable form: a duration string next to duration_ms, and "age" or
// "next_in" next to a timestamp.
func TestTextDropsOlderServerTwins(t *testing.T) {
	t.Parallel()

	got := at(`{"start":"2026-11-23T10:00:01Z","age":"5s ago","next_run":"2026-11-23T10:05:06Z","next_in":"in 5m","duration":"12.5ms","duration_ms":12.5}`)

	want := `Start     2026-11-23 10:00:01 (5s ago)
Next run  2026-11-23 10:05:06 (in 5m)
Duration  12.5ms`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextKeepsAgeWithoutATimestamp leaves an "age" member alone when no
// timestamp sits beside it.
func TestTextKeepsAgeWithoutATimestamp(t *testing.T) {
	t.Parallel()

	if got := at(`{"age":"5s ago"}`); got != "Age  5s ago" {
		t.Fatalf("got %q", got)
	}
}

// TestTextShowsRelativeTimes prints each timestamp in the zone it carries and
// its distance from now: seconds, minutes, hours with minutes, days, now and
// the future.
func TestTextShowsRelativeTimes(t *testing.T) {
	t.Parallel()

	raw := `{"now":"2026-11-23T10:00:06.4Z","sec":"2026-11-23T10:00:01.123456789Z","min":"2026-11-23T09:55:06Z",` +
		`"hour":"2026-11-23T08:55:06Z","whole":"2026-11-23T07:00:06Z","day":"2026-11-21T10:00:06Z",` +
		`"later":"2026-11-23T10:59:06Z","zone":"2026-11-23T11:00:01+01:00","text":"at 2026-11-23T10:00:01Z"}`

	want := `Now    2026-11-23 10:00:06 (now)
Sec    2026-11-23 10:00:01 (5s ago)
Min    2026-11-23 09:55:06 (5m ago)
Hour   2026-11-23 08:55:06 (1h5m ago)
Whole  2026-11-23 07:00:06 (3h ago)
Day    2026-11-21 10:00:06 (2d ago)
Later  2026-11-23 10:59:06 (in 59m)
Zone   2026-11-23 11:00:01 (5s ago)
Text   at 2026-11-23T10:00:01Z`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsLogsAsTables fits log records with short messages in one table
// whose last column holds the attributes as key=value pairs.
func TestTextPrintsLogsAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"time":"2026-11-23T10:00:01.123456789Z","level":"error","message":"charge failed","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","attrs":{"http.method":"GET","http.route":"/orders/{id}"}},` +
		`{"time":"2026-11-23T10:00:02Z","level":"info","message":"ok"}],"truncated":true,"cursor":"AbC","dropped":0,"instance_changed":false}`

	want := `TIME                          LEVEL  MESSAGE        TRACE ID                          ATTRS
2026-11-23 10:00:01 (5s ago)  error  charge failed  4bf92f3577b34da6a3ce929d0e0e4736  http.method=GET, http.route=/orders/{id}
2026-11-23 10:00:02 (4s ago)  info   ok

… more results (narrow the query, or continue with cursor AbC)`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsLogsWithLongMessagesAsBlocks falls back from a table when a
// cell before the last is a long text field, as in a failing log record.
func TestTextPrintsLogsWithLongMessagesAsBlocks(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"time":"2026-11-23T10:00:01.123456789Z","level":"error",` +
		`"message":"request failed: upstream timeout while calling billing service after 3 retries, giving up now",` +
		`"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","attrs":{"http.method":"GET","http.route":"/orders/{id}"}}],"truncated":false}`

	want := `Time      2026-11-23 10:00:01 (5s ago)
Level     error
Message   request failed: upstream timeout while calling billing service after 3 retries, giving up now
Trace ID  4bf92f3577b34da6a3ce929d0e0e4736

Attrs
  http.method  GET
  http.route   /orders/{id}`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsTraceListsAsTables fits the trace list tool's records in one
// table, with the duration in readable units.
func TestTextPrintsTraceListsAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","name":"GET /orders/{id}","start":"2026-11-23T10:00:01.123456789Z",` +
		`"duration_ms":12.5,"span_count":4,"error_count":0,"status":"ok","http_status":200}],"truncated":false}`

	want := `TRACE ID                          NAME              START                         DURATION  SPAN COUNT  ERROR COUNT  STATUS  HTTP STATUS
4bf92f3577b34da6a3ce929d0e0e4736  GET /orders/{id}  2026-11-23 10:00:01 (5s ago)  12.5ms    4           0            ok      200`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsMetricPointsAsTables prints a metric's points as a table of
// named labels below its one-line members.
func TestTextPrintsMetricPointsAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"scope":"arkoren/http","name":"http.server.requests","unit":"1","kind":"counter","keys":["route","status"],` +
		`"points":[{"labels":{"route":"/","status":"200"},"count":10,"sum":10,"last":1},{"labels":{"route":"/orders","status":"500"},"count":2,"sum":2,"last":1}]}],"truncated":false}`

	want := `Scope  arkoren/http
Name   http.server.requests
Unit   1
Kind   counter
Keys   route, status

Points
  LABELS                     COUNT  SUM  LAST
  route=/, status=200        10     10   1
  route=/orders, status=500  2      2    1`

	if got := at(raw); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsScheduleEntriesAsTables fits scheduler entries in one table
// with the description last, so a long one wraps instead of forcing blocks;
// an absent timeout leaves an empty cell and overlap is implicit.
func TestTextPrintsScheduleEntriesAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"name":"send-invoices","spec":"0 * * * *","timeout":"30s","without_overlapping":true,"next_run":"2026-11-23T11:00:00Z","description":"` +
		strings.Repeat("Send every invoice that is due. ", 4) + `"},` +
		`{"name":"reconcile","spec":"*/5 * * * *","next_run":"2026-11-23T10:05:00Z"}],"truncated":false}`

	got := at(raw)

	if !strings.HasPrefix(got, `NAME           SPEC         TIMEOUT  WITHOUT OVERLAPPING  NEXT RUN                      DESCRIPTION
send-invoices  0 * * * *    30s      true                 2026-11-23 11:00:00 (in 59m)  Send every invoice`) ||
		!strings.HasSuffix(got, "\nreconcile      */5 * * * *                                2026-11-23 10:05:00 (in 4m)") {
		t.Fatalf("got:\n%s", got)
	}
}
