package encoded

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"arkoren.dev/foundation/validate/internal/faults"
)

// identify resolves a node's $id and indexes each enclosing resource pointer.
// URI parsing errors never expose the URI or trigger external loading.
func (compiler *encodedCompiler) identify(node *Node, locations []encodedLocation) ([]encodedLocation, error) {
	resource := locations[len(locations)-1].resource

	if raw, present := node.doc["$id"]; present {
		text, ok := raw.(string)

		if !ok {
			return nil, fmt.Errorf("%w: id must be a string", faults.ErrInvalidSchema)
		}

		if len(text) > encodedMaxLocationBytes {
			return nil, fmt.Errorf("%w: id bytes", faults.ErrEncodedLimit)
		}

		identifier, err := url.Parse(text)

		if err != nil {
			return nil, fmt.Errorf("%w: invalid id URI", faults.ErrInvalidSchema)
		}

		if identifier.Fragment != "" {
			return nil, fmt.Errorf("%w: id must not contain a fragment", faults.ErrInvalidSchema)
		}

		resource = &encodedResource{
			base:    resource.base.ResolveReference(identifier),
			anchors: make(map[string]*Node),
		}
		locations = append(slices.Clone(locations), encodedLocation{resource: resource})
	}

	node.resource = resource

	for _, location := range locations {
		if err := compiler.register(location.resource.base, location.pointer, node); err != nil {
			return nil, err
		}
	}

	if err := compiler.anchors(node); err != nil {
		return nil, err
	}

	return locations, nil
}

// register adds one URI/fragment target, accepting duplicate aliases only when
// they name the identical node. Duplicate resource IDs or anchors are errors.
func (compiler *encodedCompiler) register(base *url.URL, fragment string, node *Node) error {
	if err := compiler.budget.tick(); err != nil {
		return err
	}

	identifier := *base
	identifier.Fragment = fragment
	identifier.RawFragment = ""
	key := identifier.String()

	if len(key) > encodedMaxLocationBytes {
		return fmt.Errorf("%w: schema location bytes", faults.ErrEncodedLimit)
	}

	existing := compiler.index[key]

	if existing == node {
		return nil
	}

	if existing != nil {
		return fmt.Errorf("%w: duplicate resource or anchor", faults.ErrInvalidSchema)
	}

	compiler.indexBytes += len(key)

	if compiler.indexBytes > encodedMaxIndexBytes {
		return fmt.Errorf("%w: schema index bytes", faults.ErrEncodedLimit)
	}

	compiler.index[key] = node

	return nil
}

// anchors registers static and dynamic anchor names in their local resource.
// Dynamic anchors also participate in ordinary static reference resolution.
func (compiler *encodedCompiler) anchors(node *Node) error {
	for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
		raw, present := node.doc[keyword]

		if !present {
			continue
		}

		name, ok := raw.(string)

		if !ok || !validEncodedAnchor(name) {
			return fmt.Errorf("%w: invalid anchor", faults.ErrInvalidSchema)
		}

		if err := compiler.register(node.resource.base, name, node); err != nil {
			return err
		}

		if keyword == "$dynamicAnchor" {
			node.resource.anchors[name] = node
		}
	}

	return nil
}

// validEncodedAnchor checks the ASCII plain-name fragment grammar. Pointer
// fragments are generated separately and can contain escaped arbitrary names.
func validEncodedAnchor(name string) bool {
	if name == "" {
		return false
	}

	initial := name[0]
	letter := initial >= 'a' && initial <= 'z'
	capital := initial >= 'A' && initial <= 'Z'

	if !letter && !capital && initial != '_' {
		return false
	}

	return strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._") == ""
}

// references links local references after all targets are known. Dynamic refs
// retain their anchor name only when the static target defines that dynamic
// anchor; JSON Pointers and ordinary anchors remain static as required.
func (compiler *encodedCompiler) references() error {
	for _, node := range compiler.nodes {
		if err := compiler.link(node); err != nil {
			return err
		}
	}

	return nil
}

// link resolves a node's two reference applicators without following links.
// Evaluation handles recursive graphs with per-call depth and cycle guards.
func (compiler *encodedCompiler) link(node *Node) error {
	for _, keyword := range []string{"$ref", "$dynamicRef"} {
		raw, present := node.doc[keyword]

		if !present {
			continue
		}

		if len(raw.(string)) > encodedMaxLocationBytes {
			return fmt.Errorf("%w: reference bytes", faults.ErrEncodedLimit)
		}

		reference, err := url.Parse(raw.(string))

		if err != nil {
			return fmt.Errorf("%w: invalid reference URI", faults.ErrInvalidSchema)
		}

		resolved := node.resource.base.ResolveReference(reference)
		resolved.RawFragment = ""
		target := compiler.index[resolved.String()]

		if target == nil {
			return fmt.Errorf("%w: unresolved local reference", faults.ErrInvalidSchema)
		}

		if keyword == "$ref" {
			node.reference = target
			continue
		}

		node.dynamic = target

		if target.resource.anchors[resolved.Fragment] == target {
			node.dynamicName = resolved.Fragment
		}
	}

	return nil
}
