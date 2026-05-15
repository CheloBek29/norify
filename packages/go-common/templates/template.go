package templates

import (
	"fmt"
	"regexp"
)

type Template struct {
	ID        string
	Name      string
	Body      string
	Variables []string
	Version   int
}

var variablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

func Validate(template Template) error {
	declared := make(map[string]bool, len(template.Variables))
	for _, variable := range template.Variables {
		declared[variable] = true
	}
	for _, match := range variablePattern.FindAllStringSubmatch(template.Body, -1) {
		if !declared[match[1]] {
			return fmt.Errorf("template variable %q is not declared", match[1])
		}
	}
	return nil
}

func NextVersion(current int) int {
	if current < 1 {
		return 1
	}
	return current + 1
}
