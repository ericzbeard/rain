package pkg

import (
	"testing"

	"github.com/aws-cloudformation/rain/internal/node"
	"gopkg.in/yaml.v3"
)

func TestFlattenTransform(t *testing.T) {
	input := `
Resources:
  TestResource:
    Type: AWS::CloudFormation::Module
    Properties:
      TransformedItems:
        Fn::Flatten:
          Source:
            assignment1:
              principal_name: "Admins"
              principal_type: "GROUP"
              permission_sets: ["AdministratorAccess", "ReadOnlyAccess"]
              account_ids: ["123456789012", "210987654321"]
          Transform:
            Template:
              PrincipalName: "$item.principal_name"
              PrincipalType: "$item.principal_type"
              PermissionSet: "$pset"
              AccountId: "$account"
            Variables:
              pset: "$item.permission_sets[*]"
              account: "$item.account_ids[*]"
`

	expected := `
Resources:
  TestResource:
    Type: AWS::CloudFormation::Module
    Properties:
      TransformedItems:
        - PrincipalName: "Admins"
          PrincipalType: "GROUP"
          PermissionSet: "AdministratorAccess"
          AccountId: "123456789012"
        - PrincipalName: "Admins"
          PrincipalType: "GROUP"
          PermissionSet: "AdministratorAccess"
          AccountId: "210987654321"
        - PrincipalName: "Admins"
          PrincipalType: "GROUP"
          PermissionSet: "ReadOnlyAccess"
          AccountId: "123456789012"
        - PrincipalName: "Admins"
          PrincipalType: "GROUP"
          PermissionSet: "ReadOnlyAccess"
          AccountId: "210987654321"
`

	// Parse input
	var inputNode yaml.Node
	err := yaml.Unmarshal([]byte(input), &inputNode)
	if err != nil {
		t.Fatalf("Failed to parse input YAML: %v", err)
	}

	// Parse expected
	var expectedNode yaml.Node
	err = yaml.Unmarshal([]byte(expected), &expectedNode)
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
}

func TestProcessVariableReference(t *testing.T) {
	tests := []struct {
		name     string
		template string
		context  map[string]*yaml.Node
		expected string
		wantErr  bool
	}{
		{
			name:     "Simple variable",
			template: "Hello $name",
			context: map[string]*yaml.Node{
				"name": node.MakeScalar("World"),
			},
			expected: "Hello World",
			wantErr:  false,
		},
		{
			name:     "Variable with braces",
			template: "Hello ${name}",
			context: map[string]*yaml.Node{
				"name": node.MakeScalar("World"),
			},
			expected: "Hello World",
			wantErr:  false,
		},
		{
			name:     "Variable with path",
			template: "Hello $item.name",
			context: map[string]*yaml.Node{
				"item": func() *yaml.Node {
					m := node.MakeMapping()
					m.Content = append(m.Content, node.MakeScalar("name"), node.MakeScalar("World"))
					return m
				}(),
			},
			expected: "Hello World",
			wantErr:  false,
		},
		{
			name:     "Variable with path and braces",
			template: "Hello ${item.name}",
			context: map[string]*yaml.Node{
				"item": func() *yaml.Node {
					m := node.MakeMapping()
					m.Content = append(m.Content, node.MakeScalar("name"), node.MakeScalar("World"))
					return m
				}(),
			},
			expected: "Hello World",
			wantErr:  false,
		},
		{
			name:     "Multiple variables",
			template: "$greeting $name",
			context: map[string]*yaml.Node{
				"greeting": node.MakeScalar("Hello"),
				"name":     node.MakeScalar("World"),
			},
			expected: "Hello World",
			wantErr:  false,
		},
		{
			name:     "Escaped dollar sign",
			template: "Cost: $$5",
			context:  map[string]*yaml.Node{},
			expected: "Cost: $5",
			wantErr:  false,
		},
		{
			name:     "Missing variable",
			template: "Hello $missing",
			context:  map[string]*yaml.Node{},
			expected: "Hello ",
			wantErr:  false,
		},
		{
			name:     "Non-scalar variable",
			template: "Value: $complex",
			context: map[string]*yaml.Node{
				"complex": func() *yaml.Node {
					m := node.MakeMapping()
					m.Content = append(m.Content, node.MakeScalar("key"), node.MakeScalar("value"))
					return m
				}(),
			},
			expected: "Value: &{Mapping  [&{Scalar key } &{Scalar value }]}",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := processVariableReference(tt.template, tt.context)
			if (err != nil) != tt.wantErr {
				t.Errorf("processVariableReference() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("processVariableReference() = %v, want %v", result, tt.expected)
			}
		})
	}
}
