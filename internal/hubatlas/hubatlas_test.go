package hubatlas

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"User", "user"},
		{"POST /checkout", "post-checkout"},
		{"Create Order", "create-order"},
		{"  Trailing  ", "trailing"},
		{"already-a-slug", "already-a-slug"},
		{"Order#42", "order-42"},
	}
	for _, c := range cases {
		if got := Slug(c.name); got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.name, got, c.want)
		}
		if !isSlug(Slug(c.name)) {
			t.Errorf("Slug(%q) = %q is not a valid slug", c.name, Slug(c.name))
		}
	}
}

func TestCatalogHasBothFlavors(t *testing.T) {
	cat := Catalog()
	if len(cat) != 2 {
		t.Fatalf("catalog has %d views, want 2", len(cat))
	}
	byFlavor := map[Flavor]bool{}
	for _, v := range cat {
		if v.ID == "" || v.Title == "" {
			t.Errorf("view %+v is missing id or title", v)
		}
		byFlavor[v.Flavor] = true
	}
	if !byFlavor[FlavorDataModel] || !byFlavor[FlavorAppFlows] {
		t.Errorf("catalog flavors = %v, want both data-model and app-flows", byFlavor)
	}
}

func TestViewByID(t *testing.T) {
	if v, ok := ViewByID("data-model"); !ok || v.Flavor != FlavorDataModel {
		t.Errorf("ViewByID(data-model) = %+v, %v", v, ok)
	}
	if _, ok := ViewByID("nope"); ok {
		t.Errorf("ViewByID(nope) reported known")
	}
}

func TestValidateUnknownFlavor(t *testing.T) {
	if err := Validate("mermaid", []byte(`{}`)); err == nil {
		t.Fatalf("Validate with unknown flavor should error")
	}
}
