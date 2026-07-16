// Package hubatlas owns the Atlas document contract: the View catalog and the
// per-flavor graph schemas the hub validates before storing (ADR 0013). An agent
// emits JSON conforming to a View's flavor; the hub rejects anything that breaks
// the contract before it reaches the store. Generation and rendering live in
// later slices — this package is the shared contract both sides hold to.
package hubatlas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Flavor selects the graph schema a View's document is validated against.
type Flavor string

const (
	FlavorDataModel Flavor = "data-model"
	FlavorAppFlows  Flavor = "app-flows"
)

// View is one named perspective in the Atlas. The catalog is generic: a View
// carries its id, display title, the schema flavor its document must satisfy, and
// the curated initialization prompt its generation runs from — so a future View is
// prompt plus schema flavor only (ADR 0013). GenerationPrompt composes the runnable
// prompt from Prompt and Flavor.
type View struct {
	ID     string
	Title  string
	Prompt string
	Flavor Flavor
}

// Catalog is the ordered set of Views the Atlas offers. Day one: the Data model
// and App flows Views (CONTEXT.md — View).
func Catalog() []View {
	return []View{
		{ID: "data-model", Title: "Data model", Flavor: FlavorDataModel, Prompt: dataModelPrompt},
		{ID: "app-flows", Title: "App flows", Flavor: FlavorAppFlows, Prompt: appFlowsPrompt},
	}
}

// ViewByID returns the catalog View with the given id.
func ViewByID(id string) (View, bool) {
	for _, v := range Catalog() {
		if v.ID == id {
			return v, true
		}
	}
	return View{}, false
}

// Validate reports whether document satisfies v's flavor schema.
func (v View) Validate(document []byte) error {
	return Validate(v.Flavor, document)
}

// Validate reports whether document satisfies the given flavor's schema,
// returning the first contract violation it finds.
func Validate(flavor Flavor, document []byte) error {
	switch flavor {
	case FlavorDataModel:
		return validateDataModel(document)
	case FlavorAppFlows:
		return validateAppFlows(document)
	default:
		return fmt.Errorf("unknown view flavor %q", flavor)
	}
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Slug converts a concept name to its stable kebab-case node id — the shared
// identity rule both flavors follow so a regenerated document keeps node
// identity (User → user, POST /checkout → post-checkout).
func Slug(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if b.Len() > 0 && !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// isSlug reports whether id is a well-formed kebab-case slug, the shape the
// stable-ID rule requires of every node id.
func isSlug(id string) bool {
	return slugPattern.MatchString(id)
}

// strictUnmarshal decodes data into v rejecting unknown object keys and any
// trailing content, so a document with a field outside the schema is refused.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing content after document")
	}
	return nil
}
