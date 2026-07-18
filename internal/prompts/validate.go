package prompts

import (
	"fmt"
	"strings"
	"text/template"
)

// ValidateOverride checks body as a replacement template for p: it must parse,
// render against the registry's sample values — once with every optional
// placeholder populated and once with all of them empty, so both sides of an
// {{if}} guard run — and reference every required placeholder, proven by its
// sample surviving into the rendered output. The returned error message is
// shown verbatim in the UI.
func (p Prompt) ValidateOverride(body string) error {
	t, err := template.New(p.Name).Funcs(templateFuncs).Option("missingkey=error").Parse(body)
	if err != nil {
		return fmt.Errorf("template does not parse: %s", cleanTemplateErr(p.Name, err))
	}
	for _, data := range []map[string]any{p.sampleData(true), p.sampleData(false)} {
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return renderError(p.Name, err)
		}
		out := b.String()
		for _, ph := range p.Placeholders {
			sentinel, ok := ph.Sample.(string)
			if !ph.Required || !ok {
				continue
			}
			if !strings.Contains(out, sentinel) {
				return fmt.Errorf("required placeholder {{.%s}} (%s) is missing from the template", ph.Field, ph.Description)
			}
		}
	}
	return nil
}

// sampleData maps every placeholder to its sample value; withOptional false
// zeroes the optional ones so their {{if}} guards take the empty branch.
func (p Prompt) sampleData(withOptional bool) map[string]any {
	data := make(map[string]any, len(p.Placeholders))
	for _, ph := range p.Placeholders {
		if ph.Required || withOptional {
			data[ph.Field] = ph.Sample
		} else {
			data[ph.Field] = zeroOf(ph.Sample)
		}
	}
	return data
}

func zeroOf(sample any) any {
	switch sample.(type) {
	case bool:
		return false
	case int:
		return 0
	case []string:
		return []string(nil)
	default:
		return ""
	}
}

func renderError(name string, err error) error {
	msg := err.Error()
	if i := strings.LastIndex(msg, `no entry for key "`); i >= 0 {
		key := strings.TrimSuffix(msg[i+len(`no entry for key "`):], `"`)
		return fmt.Errorf("unknown placeholder {{.%s}}", key)
	}
	return fmt.Errorf("template does not render: %s", cleanTemplateErr(name, err))
}

func cleanTemplateErr(name string, err error) string {
	msg := strings.TrimPrefix(err.Error(), "template: ")
	if rest, ok := strings.CutPrefix(msg, name+":"); ok {
		return "line " + rest
	}
	return msg
}
