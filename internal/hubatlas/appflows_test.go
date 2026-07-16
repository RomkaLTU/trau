package hubatlas

import (
	"encoding/json"
	"testing"
)

const validAppFlows = `{
  "flows": [
    {
      "id": "checkout",
      "name": "Checkout",
      "summary": "Turns a cart into an order",
      "steps": [
        {"id": "post-checkout", "name": "POST /checkout", "kind": "http"},
        {"id": "create-order", "name": "Create order", "kind": "service"}
      ],
      "edges": [
        {"from": "post-checkout", "to": "create-order", "label": "valid cart"}
      ]
    }
  ]
}`

// manyFlows marshals n valid single-step flows, for the flow-count bounds.
func manyFlows(n int) string {
	doc := AppFlows{Flows: make([]Flow, n)}
	for i := range doc.Flows {
		id := Slug(string(rune('a'+i)) + "-flow")
		doc.Flows[i] = Flow{
			ID:    id,
			Name:  "Flow",
			Steps: []Step{{ID: "start", Name: "Start", Kind: "http"}},
		}
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func TestValidateAppFlows(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr bool
	}{
		{"valid", validAppFlows, false},
		{"eight flows is fine", manyFlows(8), false},
		{"zero flows", `{"flows": []}`, true},
		{"nine flows", manyFlows(9), true},
		{
			"duplicate flow id",
			`{"flows": [
			  {"id": "checkout", "name": "A", "steps": [{"id": "s", "name": "S", "kind": "http"}]},
			  {"id": "checkout", "name": "B", "steps": [{"id": "s", "name": "S", "kind": "http"}]}
			]}`,
			true,
		},
		{
			"edge references unknown step",
			`{"flows": [{"id": "checkout", "name": "Checkout",
			  "steps": [{"id": "post-checkout", "name": "POST /checkout", "kind": "http"}],
			  "edges": [{"from": "post-checkout", "to": "ghost"}]}]}`,
			true,
		},
		{
			"duplicate step id",
			`{"flows": [{"id": "checkout", "name": "Checkout", "steps": [
			  {"id": "post-checkout", "name": "A", "kind": "http"},
			  {"id": "post-checkout", "name": "B", "kind": "service"}
			]}]}`,
			true,
		},
		{
			"unknown step kind",
			`{"flows": [{"id": "checkout", "name": "Checkout",
			  "steps": [{"id": "post-checkout", "name": "POST /checkout", "kind": "rpc"}]}]}`,
			true,
		},
		{
			"unknown field",
			`{"flows": [{"id": "checkout", "name": "Checkout", "color": "red",
			  "steps": [{"id": "s", "name": "S", "kind": "http"}]}]}`,
			true,
		},
		{
			"non-slug step id",
			`{"flows": [{"id": "checkout", "name": "Checkout",
			  "steps": [{"id": "PostCheckout", "name": "POST /checkout", "kind": "http"}]}]}`,
			true,
		},
		{"malformed json", `{"flows": `, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(FlavorAppFlows, []byte(c.doc))
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}
