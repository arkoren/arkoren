// Package openapi exports descriptive route snapshots as OpenAPI documents.
// Validation applies only to the supported envelope, not schema keywords.
package openapi

import (
	"encoding/json/v2"
	"fmt"
	"strings"

	"arkoren.dev/core/http/router/internal/snapshot"
)

// Export renders completed document settings and registration-ordered routes.
// Snapshot containers are read-only; inferred parameters enter detached copies.
func Export(document snapshot.Document, records []snapshot.Route) ([]byte, error) {
	paths, err := exportPaths(records, document.Schemes)

	if err != nil {
		return nil, err
	}

	info := map[string]any{"title": document.Title, "version": document.Version}

	if document.Description != "" {
		info["description"] = document.Description
	}

	value := map[string]any{"openapi": "3.2.1", "info": info, "paths": paths}

	if len(document.Schemes) != 0 {
		value["components"] = map[string]any{"securitySchemes": document.Schemes}
	}

	return json.Marshal(value, json.Deterministic(true))
}

// exportPaths builds path items in registration order to make first-error
// reporting stable, while the final JSON encoder sorts object keys.
func exportPaths(records []snapshot.Route, schemes map[string]any) (map[string]any, error) {
	export := &apiExport{paths: map[string]any{}, schemes: schemes,
		identities: map[string]string{}, templates: map[string]string{}}

	for _, record := range records {
		if record.Operation == nil || record.Operation.Hidden {
			continue
		}

		err := export.add(record)

		if err != nil {
			return nil, fmt.Errorf("openapi %s %s: %w", record.Method, record.Pattern, err)
		}
	}

	return export.paths, nil
}

// apiExport holds only transient document-building indexes. The router's
// records remain the single source of registrations and operation metadata.
type apiExport struct {
	// paths accumulates output Path Item objects.
	paths map[string]any
	// schemes contains explicit validated document security definitions.
	schemes map[string]any
	// identities associates operation IDs with their first route.
	identities map[string]string
	// templates associates template shape with its original spelling.
	templates map[string]string
}

// add validates a single declaration and inserts its independent operation.
func (export *apiExport) add(record snapshot.Route) error {
	if record.Operation.Err != nil {
		return record.Operation.Err
	}

	value := snapshot.Clone(record.Operation.Value).(map[string]any)

	if err := completePath(record.Pattern, value); err != nil {
		return err
	}

	if err := validateOperation(value, export.schemes); err != nil {
		return err
	}

	if err := export.claim(record, value); err != nil {
		return err
	}

	responses := value["responses"].(map[string]any)

	if len(responses) == 0 {
		delete(value, "responses")
	}

	export.insert(record, value)

	return nil
}

// claim rejects duplicate IDs and differently named equivalent path templates.
func (export *apiExport) claim(record snapshot.Route, value map[string]any) error {
	id, _ := value["operationId"].(string)

	if id != "" {
		if previous, ok := export.identities[id]; ok {
			return fmt.Errorf("duplicate operation ID %q (first at %s)", id, previous)
		}

		export.identities[id] = record.Method + " " + record.Pattern
	}

	shape := pathShape(record.Pattern)

	if previous, ok := export.templates[shape]; ok && previous != record.Pattern {
		return fmt.Errorf("path template collides with %q", previous)
	}

	export.templates[shape] = record.Pattern

	return nil
}

// insert places standard uppercase methods in their fixed fields. Other
// case-sensitive method tokens belong in additionalOperations unchanged.
func (export *apiExport) insert(record snapshot.Route, value map[string]any) {
	item, ok := export.paths[record.Pattern].(map[string]any)

	if !ok {
		item = map[string]any{}
		export.paths[record.Pattern] = item
	}

	fixed := " GET PUT POST DELETE OPTIONS HEAD PATCH TRACE QUERY "

	if strings.Contains(fixed, " "+record.Method+" ") {
		item[strings.ToLower(record.Method)] = value

		return
	}

	additional, ok := item["additionalOperations"].(map[string]any)

	if !ok {
		additional = map[string]any{}
		item["additionalOperations"] = additional
	}

	additional[record.Method] = value
}
