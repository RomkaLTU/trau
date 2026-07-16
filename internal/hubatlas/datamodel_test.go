package hubatlas

import "testing"

const validDataModel = `{
  "entities": [
    {"id": "user", "name": "User", "domain": "auth", "fields": [
      {"name": "id", "type": "uuid", "pk": true},
      {"name": "email", "type": "string"}
    ]},
    {"id": "order", "name": "Order", "domain": "commerce", "fields": [
      {"name": "id", "type": "uuid", "pk": true}
    ]}
  ],
  "relationships": [
    {"id": "user-order", "from": "user", "to": "order", "cardinality": "1:N", "label": "places"}
  ]
}`

func TestValidateDataModel(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr bool
	}{
		{"valid", validDataModel, false},
		{"no relationships is fine", `{"entities": [{"id": "user", "name": "User"}]}`, false},
		{"zero entities", `{"entities": [], "relationships": []}`, true},
		{
			"duplicate entity id",
			`{"entities": [{"id": "user", "name": "User"}, {"id": "user", "name": "Account"}]}`,
			true,
		},
		{
			"relationship endpoint references no entity",
			`{"entities": [{"id": "user", "name": "User"}],
			  "relationships": [{"id": "user-ghost", "from": "user", "to": "ghost", "cardinality": "1:N"}]}`,
			true,
		},
		{
			"duplicate relationship id",
			`{"entities": [{"id": "user", "name": "User"}, {"id": "order", "name": "Order"}],
			  "relationships": [
			    {"id": "rel", "from": "user", "to": "order", "cardinality": "1:N"},
			    {"id": "rel", "from": "order", "to": "user", "cardinality": "1:N"}
			  ]}`,
			true,
		},
		{
			"unknown cardinality",
			`{"entities": [{"id": "user", "name": "User"}, {"id": "order", "name": "Order"}],
			  "relationships": [{"id": "user-order", "from": "user", "to": "order", "cardinality": "1:many"}]}`,
			true,
		},
		{
			"unknown field",
			`{"entities": [{"id": "user", "name": "User", "color": "red"}]}`,
			true,
		},
		{
			"non-slug entity id",
			`{"entities": [{"id": "User", "name": "User"}]}`,
			true,
		},
		{
			"entity without name",
			`{"entities": [{"id": "user"}]}`,
			true,
		},
		{"malformed json", `{"entities": [`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(FlavorDataModel, []byte(c.doc))
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}
