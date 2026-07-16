package hubatlas

import "fmt"

// DataModel is the data-model flavor document: the repo's entities and the
// relationships between them, one graph.
type DataModel struct {
	Entities      []Entity       `json:"entities"`
	Relationships []Relationship `json:"relationships"`
}

// Entity is one node in the data model. Domain is a free grouping key used for
// layout clustering; it carries no identity.
type Entity struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Domain string  `json:"domain"`
	Fields []Field `json:"fields"`
}

// Field is one attribute of an Entity.
type Field struct {
	Name string `json:"name"`
	Type string `json:"type"`
	PK   bool   `json:"pk"`
}

// Relationship is a directed edge between two entities, identified by their ids.
type Relationship struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Cardinality string `json:"cardinality"`
	Label       string `json:"label"`
}

func validCardinality(c string) bool {
	switch c {
	case "1:1", "1:N", "N:M":
		return true
	default:
		return false
	}
}

func validateDataModel(document []byte) error {
	var doc DataModel
	if err := strictUnmarshal(document, &doc); err != nil {
		return err
	}
	if len(doc.Entities) == 0 {
		return fmt.Errorf("data model has no entities")
	}

	entities := make(map[string]bool, len(doc.Entities))
	for _, e := range doc.Entities {
		if !isSlug(e.ID) {
			return fmt.Errorf("entity id %q is not a kebab-case slug", e.ID)
		}
		if entities[e.ID] {
			return fmt.Errorf("duplicate entity id %q", e.ID)
		}
		entities[e.ID] = true
		if e.Name == "" {
			return fmt.Errorf("entity %q has no name", e.ID)
		}
		for _, f := range e.Fields {
			if f.Name == "" {
				return fmt.Errorf("entity %q has a field with no name", e.ID)
			}
		}
	}

	rels := make(map[string]bool, len(doc.Relationships))
	for _, r := range doc.Relationships {
		if !isSlug(r.ID) {
			return fmt.Errorf("relationship id %q is not a kebab-case slug", r.ID)
		}
		if rels[r.ID] {
			return fmt.Errorf("duplicate relationship id %q", r.ID)
		}
		rels[r.ID] = true
		if !entities[r.From] {
			return fmt.Errorf("relationship %q references unknown entity %q", r.ID, r.From)
		}
		if !entities[r.To] {
			return fmt.Errorf("relationship %q references unknown entity %q", r.ID, r.To)
		}
		if !validCardinality(r.Cardinality) {
			return fmt.Errorf("relationship %q has unknown cardinality %q", r.ID, r.Cardinality)
		}
	}
	return nil
}
