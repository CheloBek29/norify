package templates

import "testing"

func TestValidateTemplateVariables(t *testing.T) {
	tpl := Template{Body: "Hello {{first_name}}, order {{order_id}} is ready", Variables: []string{"first_name", "order_id"}}
	if err := Validate(tpl); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
}

func TestValidateTemplateVariablesRejectsMissingDeclaration(t *testing.T) {
	tpl := Template{Body: "Hello {{first_name}}, order {{order_id}} is ready", Variables: []string{"first_name"}}
	if err := Validate(tpl); err == nil {
		t.Fatal("expected missing variable error")
	}
}

func TestNextVersion(t *testing.T) {
	if got := NextVersion(4); got != 5 {
		t.Fatalf("expected version 5, got %d", got)
	}
}
