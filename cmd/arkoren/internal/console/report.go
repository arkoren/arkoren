package console

// Action is the kind of one [Change]. The text presentation colors each kind,
// and the string values are part of the JSON contract.
type Action string

// The actions of a [Change].
const (
	// ActionCreate is a new file.
	ActionCreate Action = "create"

	// ActionEdit is an insertion into an existing file.
	ActionEdit Action = "edit"

	// ActionRemove is a deleted file.
	ActionRemove Action = "remove"

	// ActionWrite is a file written over a previous version, such as a skill
	// update.
	ActionWrite Action = "write"

	// ActionRequire is a module pinned in go.mod; the path is module@version.
	ActionRequire Action = "require"

	// ActionTidy is a go mod tidy run; the path is go.mod.
	ActionTidy Action = "tidy"
)

// Change is one effect of a local action, reported in the top-level "changes"
// array of a result next to its "summary". Local action results order their
// fields summary, ok or dry_run, command fields, changes, next_steps; the
// text presentation relies on this shape, see [Text].
type Change struct {
	// Action says what happened to Path.
	Action Action `json:"action"`

	// Path is a slash-separated, project-relative file path, or module@version
	// for [ActionRequire].
	Path string `json:"path"`

	// Detail adds context such as the inserted line or "private key". It is
	// omitted when empty.
	Detail string `json:"detail,omitzero"`
}
