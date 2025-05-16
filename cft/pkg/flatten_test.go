package pkg

import (
	"strings"
	"testing"

	"github.com/aws-cloudformation/rain/internal/node"
	"gopkg.in/yaml.v3"
)

func TestFnFlatten(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "Simple list source",
			input: `
Fn::Flatten:
  - item1
  - item2
  - item3
`,
			expected: `
- item1
- item2
- item3
`,
		},
		{
			name: "Simple scalar source",
			input: `
Fn::Flatten: scalar
`,
			expected: `
- scalar
`,
		},
		{
			name: "Nested structure with pattern",
			input: `
Fn::Flatten:
  Source:
    Group1:
      items:
        - id: "item1"
          value: "value1"
        - id: "item2"
          value: "value2"
    Group2:
      items:
        - id: "item3"
          value: "value3"
  Pattern: "$.*.items[*]"
`,
			expected: `
- id: "item1"
  value: "value1"
- id: "item2"
  value: "value2"
- id: "item3"
  value: "value3"
`,
		},
		{
			name: "Transform with template",
			input: `
Fn::Flatten:
  Source:
    - name: "service1"
      type: "api"
    - name: "service2"
      type: "worker"
  Transform:
    Template:
      ServiceName: "$item.name"
      ServiceType: "$item.type"
`,
			expected: `
- ServiceName: "service1"
  ServiceType: "api"
- ServiceName: "service2"
  ServiceType: "worker"
`,
		},
		{
			name: "Transform with variables (cross-product)",
			input: `
Fn::Flatten:
  Source:
    - name: "service1"
      environments:
        - "dev"
        - "prod"
    - name: "service2"
      environments:
        - "dev"
  Transform:
    Template:
      ServiceName: "$item.name"
      Environment: "$env"
    Variables:
      env: "$item.environments[*]"
`,
			expected: `
- ServiceName: "service1"
  Environment: "dev"
- ServiceName: "service1"
  Environment: "prod"
- ServiceName: "service2"
  Environment: "dev"
`,
		},
		{
			name: "Group by attribute",
			input: `
Fn::Flatten:
  Source:
    - UserName: "john.doe"
      GroupName: "Admins"
    - UserName: "john.doe"
      GroupName: "Developers"
    - UserName: "jane.smith"
      GroupName: "Developers"
  GroupBy: "UserName"
`,
			expected: `
john.doe:
  - UserName: "john.doe"
    GroupName: "Admins"
  - UserName: "john.doe"
    GroupName: "Developers"
jane.smith:
  - UserName: "jane.smith"
    GroupName: "Developers"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse input
			var inputNode yaml.Node
			err := yaml.Unmarshal([]byte(tt.input), &inputNode)
			if err != nil {
				t.Fatalf("Failed to parse input YAML: %v", err)
			}

			// Parse expected
			var expectedNode yaml.Node
			err = yaml.Unmarshal([]byte(tt.expected), &expectedNode)
			if err != nil {
				t.Fatalf("Failed to parse expected YAML: %v", err)
			}

			// Process Fn::Flatten
			err = FnFlatten(&inputNode)
			if err != nil {
				t.Fatalf("FnFlatten returned error: %v", err)
			}

			// Compare results
			inputStr := node.YamlStr(&inputNode)
			expectedStr := node.YamlStr(&expectedNode)
			if inputStr != expectedStr {
				t.Errorf("FnFlatten result doesn't match expected.\nGot:\n%s\nExpected:\n%s",
					inputStr, expectedStr)
			}
		})
	}
}

func TestExtractByPattern(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		pattern  string
		expected []string
	}{
		{
			name: "Root pattern",
			source: `
key: value
`,
			pattern: "$",
			expected: []string{
				`key: value`,
			},
		},
		{
			name: "Simple property access",
			source: `
key: value
`,
			pattern: "$.key",
			expected: []string{
				"value",
			},
		},
		{
			name: "Wildcard for dictionary",
			source: `
key1: value1
key2: value2
`,
			pattern: "$.*",
			expected: []string{
				"value1",
				"value2",
			},
		},
		{
			name: "Array access",
			source: `
items:
  - item1
  - item2
`,
			pattern: "$.items[1]",
			expected: []string{
				"item2",
			},
		},
		{
			name: "Array wildcard",
			source: `
items:
  - item1
  - item2
`,
			pattern: "$.items[*]",
			expected: []string{
				"item1",
				"item2",
			},
		},
		{
			name: "Nested property with array",
			source: `
groups:
  group1:
    items:
      - id: item1
        value: value1
      - id: item2
        value: value2
  group2:
    items:
      - id: item3
        value: value3
`,
			pattern: "$.groups.*.items[*].id",
			expected: []string{
				"item1",
				"item2",
				"item3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse source
			var sourceNode yaml.Node
			err := yaml.Unmarshal([]byte(tt.source), &sourceNode)
			if err != nil {
				t.Fatalf("Failed to parse source YAML: %v", err)
			}

			// Extract by pattern
			result := ExtractByPattern(&sourceNode, tt.pattern)

			// Check result length
			if len(result) != len(tt.expected) {
				t.Fatalf("Expected %d results, got %d", len(tt.expected), len(result))
			}

			// For root pattern test, we need to compare the YAML string
			if tt.pattern == "$" {
				resultStr := node.YamlStr(result[0])
				// Trim leading/trailing whitespace for comparison
				resultStr = strings.TrimSpace(resultStr)
				expected := strings.TrimSpace(tt.expected[0])
				if resultStr != expected {
					t.Errorf("Expected '%s', got '%s'", expected, resultStr)
				}
				return
			}

			// Convert result nodes to strings for comparison
			resultStrs := make([]string, len(result))
			for j, n := range result {
				if n.Kind == yaml.ScalarNode {
					resultStrs[j] = n.Value
				} else {
					// For non-scalar nodes, convert to YAML string
					resultStrs[j] = node.YamlStr(n)
				}
			}

			// Compare results
			for _, expected := range tt.expected {
				found := false
				for _, resultStr := range resultStrs {
					if resultStr == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected result not found: %s", expected)
				}
			}
		})
	}
}
