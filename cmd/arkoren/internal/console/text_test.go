package console_test

import (
	"strings"
	"testing"

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

// TestTextWritesErrorHeadline prints the message in red, the members, and the
// hint last and dim.
func TestTextWritesErrorHeadline(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"error":{"message":"boom","hint":"retry","status":500}}`), true))

	want := "\x1b[1m\x1b[31m✗ boom\x1b[0m\n\n" +
		console.ColorKey + "Status" + console.ColorReset + "  " + console.ColorNumber + "500" + console.ColorReset + "\n\n" +
		"\x1b[2mretry\x1b[0m"

	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestTextWritesWarningHeadline prints a yellow exclamation mark.
func TestTextWritesWarningHeadline(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"warning":{"message":"careful","action":"trust","effects":["It does one thing","It does another"]}}`), false))

	want := `! careful

Action  trust

Effects
  - It does one thing
  - It does another`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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

// TestTextNumbersNextSteps prints a heading and numbered lines.
func TestTextNumbersNextSteps(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"summary":"Done","next_steps":["cd app","make run"]}`), false))

	want := `✓ Done

Next steps
  1. cd app
  2. make run`

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

// TestTextListsNestedArrays bullets one-line elements and prints nested
// elements as blocks.
func TestTextListsNestedArrays(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`["a",{"b":{"c":1}},2]`), false))

	want := `- a

B
  C  1

- 2`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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

// TestTextNamesTheNextCursor shows the continuation cursor of a cursor list only
// when more records exist, and keeps a nonzero dropped count visible.
func TestTextNamesTheNextCursor(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"name":"a"}],"truncated":false,"cursor":"AbC123","dropped":4,"has_more":true,"instance_changed":false}`
	got := string(console.Text([]byte(raw), false))

	want := `NAME
a

Dropped  4

… more results (continue with cursor AbC123)`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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

// TestTextKeepsHealthStatusAboveItsChecks puts the verdict before the check
// table of the health tool, without an Items heading.
func TestTextKeepsHealthStatusAboveItsChecks(t *testing.T) {
	t.Parallel()

	raw := `{"status":"failing","items":[` +
		`{"name":"database","status":"ok","since":"2026-11-23T09:00:00.5Z","checked_at":"2026-11-23T10:00:00.25Z","age":"3s","fresh":true},` +
		`{"name":"cache","status":"failing","error":"connection refused","since":"2026-11-23T09:59:00Z","checked_at":"2026-11-23T10:00:00Z","age":"3s","fresh":true}]}`
	got := string(console.Text([]byte(raw), false))

	want := `Status  failing

NAME      STATUS   SINCE                 CHECKED AT            AGE  FRESH  ERROR
database  ok       2026-11-23T09:00:00Z  2026-11-23T10:00:00Z  3s   true
cache     failing  2026-11-23T09:59:00Z  2026-11-23T10:00:00Z  3s   true   connection refused`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextHidesMachineTwins omits duration_ms and the byte counts that sit
// beside their human-readable twins, as the info and scheduler tools return.
func TestTextHidesMachineTwins(t *testing.T) {
	t.Parallel()

	raw := `{"name":"reconcile","started_at":"2026-11-23T10:00:00Z","duration_ms":1500,"duration":"1.5s","runtime":{"heap_alloc_bytes":1048576,"heap_alloc":"1.0 MiB","num_gc":3}}`
	got := string(console.Text([]byte(raw), false))

	want := `Name        reconcile
Started at  2026-11-23T10:00:00Z
Duration    1.5s

Runtime
  Heap alloc  1.0 MiB
  Num gc      3`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextKeepsTwinsWithoutAReadableMember keeps a *_ms number that has no
// string member beside it.
func TestTextKeepsTwinsWithoutAReadableMember(t *testing.T) {
	t.Parallel()

	if got := string(console.Text([]byte(`{"duration_ms":1500}`), false)); got != "Duration ms  1500" {
		t.Fatalf("got %q", got)
	}
}

// TestTextTrimsFractionalSeconds shortens nanosecond RFC 3339 timestamps and
// leaves other strings alone.
func TestTextTrimsFractionalSeconds(t *testing.T) {
	t.Parallel()

	got := string(console.Text([]byte(`{"a":"2026-11-23T10:00:01.123456789Z","b":"2026-11-23T10:00:01.5+01:00","c":"2026-11-23T10:00:01Z","d":"at 2026-11-23T10:00:01.5Z"}`), false))

	want := `A  2026-11-23T10:00:01Z
B  2026-11-23T10:00:01+01:00
C  2026-11-23T10:00:01Z
D  at 2026-11-23T10:00:01.5Z`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsLogsWithLongMessagesAsBlocks falls back from a table when a
// cell is a long text field, as in a failing log record, and prints the
// attributes as a section.
func TestTextPrintsLogsWithLongMessagesAsBlocks(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"time":"2026-11-23T10:00:01.123456789Z","age":"5s","level":"error",` +
		`"message":"request failed: upstream timeout while calling billing service after 3 retries, giving up now",` +
		`"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","attrs":{"http.method":"GET","http.route":"/orders/{id}"}}],"truncated":false}`
	got := string(console.Text([]byte(raw), false))

	want := `Time      2026-11-23T10:00:01Z
Age       5s
Level     error
Message   request failed: upstream timeout while calling billing service after 3 retries, giving up now
Trace ID  4bf92f3577b34da6a3ce929d0e0e4736

Attrs
  http.method  GET
  http.route   /orders/{id}`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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

// TestTextPrintsTraceListsAsTables fits the trace list tool's records in one
// table, with the duration in its readable form only.
func TestTextPrintsTraceListsAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","name":"GET /orders/{id}","start":"2026-11-23T10:00:01.123456789Z","age":"5s",` +
		`"duration_ms":12.5,"duration":"12.5ms","span_count":4,"error_count":0,"status":"ok","http_status":200}],"truncated":false}`
	got := string(console.Text([]byte(raw), false))

	want := `TRACE ID                          NAME              START                 AGE  DURATION  SPAN COUNT  ERROR COUNT  STATUS  HTTP STATUS
4bf92f3577b34da6a3ce929d0e0e4736  GET /orders/{id}  2026-11-23T10:00:01Z  5s   12.5ms    4           0            ok      200`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextNestsMetricPointsAsTables prints a metric's points as a table below
// its one-line members.
func TestTextNestsMetricPointsAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"scope":"arkoren/http","name":"http.server.requests","unit":"1","kind":"counter","keys":["route","status"],` +
		`"points":[{"values":["/","200"],"count":10,"sum":10,"last":1},{"values":["/orders","500"],"count":2,"sum":2,"last":1}]}],"truncated":false}`
	got := string(console.Text([]byte(raw), false))

	want := `Scope  arkoren/http
Name   http.server.requests
Unit   1
Kind   counter
Keys   route, status

Points
  VALUES        COUNT  SUM  LAST
  /, 200        10     10   1
  /orders, 500  2      2    1`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTextPrintsScheduleEntriesAsTables fits scheduler entries in one table
// when they are short, leaving a missing timeout cell empty.
func TestTextPrintsScheduleEntriesAsTables(t *testing.T) {
	t.Parallel()

	raw := `{"items":[{"name":"send-invoices","spec":"0 * * * *","timeout":"30s","without_overlapping":true,"next_run":"2026-11-23T11:00:00Z","next_in":"59m"},` +
		`{"name":"reconcile","spec":"*/5 * * * *","without_overlapping":false,"next_run":"2026-11-23T10:05:00Z","next_in":"4m"}],"truncated":false}`
	got := string(console.Text([]byte(raw), false))

	want := `NAME           SPEC         TIMEOUT  WITHOUT OVERLAPPING  NEXT RUN              NEXT IN
send-invoices  0 * * * *    30s      true                 2026-11-23T11:00:00Z  59m
reconcile      */5 * * * *           false                2026-11-23T10:05:00Z  4m`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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
