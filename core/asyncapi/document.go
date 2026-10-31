package asyncapi

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"sync"

	"arkoren.dev/core/asyncapi/internal/graph"
)

// Document is an immutable asynchronous API description. Its zero value is
// invalid; use New. Export results, including errors, are cached concurrently.
//
// Example:
//
//	document := asyncapi.New("Events", "1")
//	data, err := document.JSON()
//	if err != nil {
//		log.Fatal(err)
//	}
//	_ = data
type Document struct {
	// load owns the immutable encoded document and its once-only validation.
	load func() ([]byte, error)
}

// Builder declares document metadata and graph nodes. It is only a construction
// draft; retained drafts are detached from published documents.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Description("Application event contract")
//	})
type Builder struct {
	// draft holds the document graph and the first construction error.
	draft
}

// BuilderFunc configures a fresh document draft synchronously. Callbacks run
// once, in order, even when inputs are invalid; nil entries panic.
//
// Example:
//
//	var configure asyncapi.BuilderFunc = func(definition *asyncapi.Builder) {
//		definition.Description("Public event contracts")
//	}
//	_ = asyncapi.New("Events", "1", configure)
type BuilderFunc = func(*Builder)

// draft stores a mutable JSON object and its first error. Only captured JSON
// values enter the graph, so publication never retains caller-owned examples.
type draft struct {
	// object contains the JSON members of this graph node.
	object map[string]any

	// err preserves the first invalid declaration on this draft.
	err error
}

// New captures an AsyncAPI 3.1 description without I/O. All supplied callbacks
// run once in order, including invalid inputs; nil entries panic. Required title
// and version and graph errors are reported by JSON and Handler, not by New.
//
// Example:
//
//	document := asyncapi.New("Billing", "1.0", func(definition *asyncapi.Builder) {
//		definition.Description("Billing event contracts")
//	})
//	_ = document.Handler()
func New(title, version string, configure ...BuilderFunc) *Document {
	definition := &Builder{
		draft: draft{
			object: map[string]any{
				"asyncapi":   "3.1.0",
				"info":       map[string]any{"title": title, "version": version},
				"channels":   map[string]any{},
				"operations": map[string]any{},
				"servers":    map[string]any{},
				"components": map[string]any{
					"securitySchemes": map[string]any{},
					"schemas":         map[string]any{},
				},
			},
		},
	}

	for _, configure := range configure {
		configure(definition)
	}

	data, err := json.Marshal(definition.object, json.Deterministic(true))
	definition.fail(err)

	err = definition.err

	return &Document{
		load: sync.OnceValues(func() ([]byte, error) {
			if err != nil {
				return nil, fmt.Errorf("asyncapi: %w", err)
			}

			if err := graph.Validate(data); err != nil {
				return nil, fmt.Errorf("asyncapi: %w", err)
			}

			return data, nil
		}),
	}
}

// JSON returns a detached copy of the deterministic document bytes. Both
// successful validation and failures are cached once. Nil and zero Documents
// return an error. Mutating the returned bytes never affects future exports.
//
// Example:
//
//	data, err := asyncapi.New("Events", "1").JSON()
//	if err != nil {
//		log.Fatal(err)
//	}
//	_ = data
func (document *Document) JSON() ([]byte, error) {
	if document == nil || document.load == nil {
		return nil, fmt.Errorf("asyncapi: document must be constructed with New")
	}

	data, err := document.load()

	return bytes.Clone(data), err
}

// Description replaces the information object's human-readable description.
// An empty string is accepted; this setting does not affect runtime messaging.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Description("Public integration contract")
//	})
func (definition *Builder) Description(text string) *Builder {
	definition.object["info"].(map[string]any)["description"] = text

	return definition
}

// fail keeps the first construction error without suppressing later callbacks.
// Nil errors leave the current diagnostic unchanged.
func (draft *draft) fail(err error) {
	if draft.err == nil {
		draft.err = err
	}
}

// insert captures a graph child by encoding it immediately. This detaches every
// owned container even if callers retain the mutable child construction draft.
func (draft *draft) insert(collection, id string, child draft) {
	draft.fail(child.err)
	draft.fail(validateID(id))

	items := draft.object[collection].(map[string]any)

	if _, exists := items[id]; exists {
		draft.fail(fmt.Errorf("duplicate %s id %q", collection, id))

		return
	}

	data, err := json.Marshal(child.object, json.Deterministic(true))
	draft.fail(err)

	items[id] = jsontext.Value(data)
}
