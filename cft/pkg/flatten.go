package pkg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aws-cloudformation/rain/cft/visitor"
	"github.com/aws-cloudformation/rain/internal/node"
	"github.com/aws-cloudformation/rain/internal/s11n"
	"gopkg.in/yaml.v3"
)

// FnFlatten implements the Fn::Flatten intrinsic function
// This function flattens nested structures and can transform and group the results
func FnFlatten(n *yaml.Node) error {
	var err error
	vf := func(v *visitor.Visitor) {
		vn := v.GetYamlNode()
		if vn.Kind != yaml.MappingNode {
			return
		}
		if len(vn.Content) < 2 {
			return
		}
		if vn.Content[0].Value != "Fn::Flatten" {
			return
		}

		// Get the flatten configuration
		flattenConfig := vn.Content[1]
		if flattenConfig.Kind != yaml.MappingNode {
			// Handle simple source case (just a list or scalar)
			if flattenConfig.Kind == yaml.SequenceNode {
				// If it's already a list, just return it as is
				*vn = *flattenConfig
				return
			}
			// For scalar, wrap in a list
			result := node.MakeSequence([]string{})
			result.Content = append(result.Content, node.Clone(flattenConfig))
			*vn = *result
			return
		}

		// Extract parameters from the configuration
		_, sourceNode, sourceErr := s11n.GetMapValue(flattenConfig, "Source")
		_, patternNode, patternErr := s11n.GetMapValue(flattenConfig, "Pattern")
		_, transformNode, transformErr := s11n.GetMapValue(flattenConfig, "Transform")
		_, groupByNode, groupByErr := s11n.GetMapValue(flattenConfig, "GroupBy")

		// Handle empty or invalid source
		if sourceErr != nil || sourceNode == nil {
			err = fmt.Errorf("Fn::Flatten requires a Source parameter")
			v.Stop()
			return
		}

		// Extract values using pattern if provided
		var items []*yaml.Node
		if patternErr == nil && patternNode != nil && patternNode.Kind == yaml.ScalarNode {
			pattern := patternNode.Value
			items = ExtractByPattern(sourceNode, pattern)
		} else if sourceNode.Kind == yaml.SequenceNode {
			// If source is already a list, use it directly
			items = sourceNode.Content
		} else {
			// For non-list sources without a pattern, extract items
			items = extractItemsFromSource(sourceNode)
		}

		// Apply transformation if provided
		if transformErr == nil && transformNode != nil && transformNode.Kind == yaml.MappingNode {
			_, templateNode, templateErr := s11n.GetMapValue(transformNode, "Template")
			_, variablesNode, _ := s11n.GetMapValue(transformNode, "Variables")

			if templateErr != nil || templateNode == nil {
				err = fmt.Errorf("Fn::Flatten Transform requires a Template parameter")
				v.Stop()
				return
			}

			items, err = applyTransform(items, templateNode, variablesNode)
			if err != nil {
				v.Stop()
				return
			}
		}

		// Group by attribute if provided
		if groupByErr == nil && groupByNode != nil && groupByNode.Kind == yaml.ScalarNode {
			groupBy := groupByNode.Value
			result := groupByAttribute(items, groupBy)
			*vn = *result
			return
		}

		// Create the result sequence
		result := node.MakeSequence([]string{})
		for _, item := range items {
			result.Content = append(result.Content, node.Clone(item))
		}
		*vn = *result
	}
	visitor.NewVisitor(n).Visit(vf)
	return err
}

// ExtractByPattern extracts values from a nested structure using a JSONPath-like pattern
func ExtractByPattern(source *yaml.Node, pattern string) []*yaml.Node {
	// Handle empty or root pattern
	if pattern == "" || pattern == "$" {
		return []*yaml.Node{source}
	}

	// Special handling for the document node
	if source.Kind == yaml.DocumentNode && len(source.Content) > 0 {
		return ExtractByPattern(source.Content[0], pattern)
	}

	// Normalize the pattern by removing leading $ and dot
	if strings.HasPrefix(pattern, "$") {
		pattern = pattern[1:]
		pattern = strings.TrimPrefix(pattern, ".")
	}

	// If pattern is empty after normalization, return the source
	if pattern == "" {
		return []*yaml.Node{source}
	}

	// For complex patterns with nested properties, handle them specially
	if strings.Contains(pattern, ".") && !strings.HasPrefix(pattern, "[") {
		parts := strings.SplitN(pattern, ".", 2)
		firstPart := parts[0]
		restParts := parts[1]

		// Process the first part to get intermediate values
		intermediateValues := ExtractByPattern(source, firstPart)

		// If no intermediate values, return empty result
		if len(intermediateValues) == 0 {
			return []*yaml.Node{}
		}

		// Process the rest of the pattern for each intermediate value
		var results []*yaml.Node
		for _, intermediateValue := range intermediateValues {
			subResults := ExtractByPattern(intermediateValue, restParts)
			results = append(results, subResults...)
		}

		return results
	}

	// Handle array access with property (e.g., "items[*].id")
	if strings.Contains(pattern, "[") && strings.Contains(pattern, "].") {
		beforeBracket := pattern[:strings.Index(pattern, "[")]
		bracketPart := pattern[strings.Index(pattern, "[") : strings.Index(pattern, "].")+1]
		afterBracket := pattern[strings.Index(pattern, "].")+2:]

		// First get the array
		arrayValues := ExtractByPattern(source, beforeBracket+bracketPart)

		// Then extract the property from each array item
		var results []*yaml.Node
		for _, arrayItem := range arrayValues {
			subResults := ExtractByPattern(arrayItem, afterBracket)
			results = append(results, subResults...)
		}

		return results
	}

	// For simple patterns, use the standard approach
	var results []*yaml.Node

	// Handle wildcard for dictionaries
	if pattern == "*" && source.Kind == yaml.MappingNode {
		for i := 1; i < len(source.Content); i += 2 {
			results = append(results, source.Content[i])
		}
		return results
	}

	// Handle property access with array notation (e.g., "items[*]" or "items[0]")
	if strings.Contains(pattern, "[") && strings.HasSuffix(pattern, "]") {
		propName := pattern[:strings.Index(pattern, "[")]
		arrayPattern := pattern[strings.Index(pattern, "["):]

		// Find the property in the mapping
		if source.Kind == yaml.MappingNode {
			for i := 0; i < len(source.Content); i += 2 {
				if source.Content[i].Value == propName {
					propValue := source.Content[i+1]

					// Handle array wildcard
					if arrayPattern == "[*]" && propValue.Kind == yaml.SequenceNode {
						return propValue.Content
					}

					// Handle specific index
					if strings.HasPrefix(arrayPattern, "[") && strings.HasSuffix(arrayPattern, "]") {
						indexStr := arrayPattern[1 : len(arrayPattern)-1]
						if index, err := strconv.Atoi(indexStr); err == nil {
							if propValue.Kind == yaml.SequenceNode && index >= 0 && index < len(propValue.Content) {
								return []*yaml.Node{propValue.Content[index]}
							}
						}
					}
				}
			}
		}
		return results
	}

	// Handle specific property access
	if source.Kind == yaml.MappingNode {
		for i := 0; i < len(source.Content); i += 2 {
			if source.Content[i].Value == pattern {
				return []*yaml.Node{source.Content[i+1]}
			}
		}
	}

	// Handle array wildcard
	if pattern == "[*]" && source.Kind == yaml.SequenceNode {
		return source.Content
	}

	// Handle specific array index
	if strings.HasPrefix(pattern, "[") && strings.HasSuffix(pattern, "]") {
		indexStr := pattern[1 : len(pattern)-1]
		if index, err := strconv.Atoi(indexStr); err == nil {
			if source.Kind == yaml.SequenceNode && index >= 0 && index < len(source.Content) {
				return []*yaml.Node{source.Content[index]}
			}
		}
	}

	return results
}

// extractItemsFromSource extracts items from a non-list source without a pattern
func extractItemsFromSource(source *yaml.Node) []*yaml.Node {
	if source.Kind != yaml.MappingNode {
		return []*yaml.Node{source}
	}

	// Convert the dict to a list of dicts
	var result []*yaml.Node
	for i := 0; i < len(source.Content); i += 2 {
		key := source.Content[i]
		value := source.Content[i+1]

		// If the value is a mapping, add it directly to the result
		// This ensures that $item.property references work correctly
		if value.Kind == yaml.MappingNode {
			result = append(result, value)
		} else if value.Kind == yaml.SequenceNode {
			for _, item := range value.Content {
				newItem := node.MakeMapping()
				newItem.Content = append(newItem.Content, node.Clone(key), node.Clone(item))
				result = append(result, newItem)
			}
		} else {
			newItem := node.MakeMapping()
			newItem.Content = append(newItem.Content, node.Clone(key), node.Clone(value))
			result = append(result, newItem)
		}
	}

	return result
}

// applyTransform applies transformation to each item in the list
func applyTransform(items []*yaml.Node, templateNode *yaml.Node, variablesNode *yaml.Node) ([]*yaml.Node, error) {
	if templateNode == nil {
		return nil, fmt.Errorf("Fn::Flatten Transform requires a Template parameter")
	}

	var variables map[string]*yaml.Node
	if variablesNode != nil && variablesNode.Kind == yaml.MappingNode {
		variables = make(map[string]*yaml.Node)
		for i := 0; i < len(variablesNode.Content); i += 2 {
			varName := variablesNode.Content[i].Value
			varPattern := variablesNode.Content[i+1]
			if varPattern.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("Fn::Flatten Transform variable pattern for '%s' must be a string", varName)
			}
			variables[varName] = varPattern
		}
	}

	var result []*yaml.Node

	// Process each item and extract nested structures if needed
	var processedItems []*yaml.Node
	for _, item := range items {
		processedItems = append(processedItems, processItemForTransform(item)...)
	}

	// Process each item with the transform
	for _, item := range processedItems {
		// Determine the actual item to use for variable extraction
		actualItem := item
		if item.Kind == yaml.MappingNode && len(item.Content) == 2 {
			// If the item is a mapping with a single key-value pair and the value is a mapping,
			// use the value as the actual item for variable extraction
			if item.Content[0].Kind == yaml.ScalarNode && item.Content[1].Kind == yaml.MappingNode {
				actualItem = item.Content[1]
			}
		}

		// If no variables, just apply the template directly
		if len(variables) == 0 {
			context := map[string]*yaml.Node{
				"item": actualItem,
			}
			transformed, err := applyTemplate(actualItem, templateNode, context)
			if err != nil {
				return nil, err
			}
			result = append(result, transformed)
			continue
		}

		// Extract variable values
		varValues, err := extractVariableValues(actualItem, variables)
		if err != nil {
			return nil, err
		}

		// Skip items that don't have all required variables
		allVarsHaveValues := true
		for _, values := range varValues {
			if len(values) == 0 {
				allVarsHaveValues = false
				break
			}
		}
		if !allVarsHaveValues {
			continue
		}

		// Generate cross product of all variable combinations
		combinations := generateCombinations(varValues)

		// Apply template for each combination
		for _, combo := range combinations {
			context := map[string]*yaml.Node{
				"item": actualItem,
			}
			for k, v := range combo {
				context[k] = v
			}
			transformed, err := applyTemplate(actualItem, templateNode, context)
			if err != nil {
				return nil, err
			}
			result = append(result, transformed)
		}
	}

	return result, nil
}

// processItemForTransform processes an item to extract nested structures if needed
func processItemForTransform(item *yaml.Node) []*yaml.Node {
	result := []*yaml.Node{item} // Always include the original item

	if item.Kind != yaml.MappingNode {
		return result
	}

	// If the item is a dictionary with a single key and that value is a
	// list or dict, we might need to process its contents
	if len(item.Content) == 2 && item.Content[0].Kind == yaml.ScalarNode {
		// We don't actually use the key
		value := item.Content[1]

		// For dictionaries with nested structure, consider its nested content
		if value.Kind == yaml.SequenceNode {
			// Add list items individually
			result = append(result, value.Content...)
		} else if value.Kind == yaml.MappingNode {
			// For nested dictionaries, include the value itself as an item
			// This ensures that $item.property references work correctly
			result = append(result, value)
			
			// Also include any list properties
			for i := 0; i < len(value.Content); i += 2 {
				if value.Content[i].Kind == yaml.ScalarNode && i+1 < len(value.Content) {
					propValue := value.Content[i+1]
					if propValue.Kind == yaml.SequenceNode {
						result = append(result, propValue.Content...)
					}
				}
			}
		}
	}

	return result
}

// extractVariableValues extracts variable values from an item based on variable patterns
func extractVariableValues(actualItem *yaml.Node, variables map[string]*yaml.Node) (map[string][]*yaml.Node, error) {
	varValues := make(map[string][]*yaml.Node)
	for varName, varPatternNode := range variables {
		if varPatternNode.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("Fn::Flatten Transform variable pattern for '%s' must be a string", varName)
		}
		varPattern := varPatternNode.Value

		// Handle special case for direct property access with [*]
		if strings.HasPrefix(varPattern, "$item.") && strings.HasSuffix(varPattern, "[*]") {
			propName := varPattern[6 : len(varPattern)-3] // Extract property name between $item. and [*]
			if actualItem.Kind == yaml.MappingNode {
				for i := 0; i < len(actualItem.Content); i += 2 {
					if actualItem.Content[i].Value == propName &&
						i+1 < len(actualItem.Content) &&
						actualItem.Content[i+1].Kind == yaml.SequenceNode {
						varValues[varName] = actualItem.Content[i+1].Content
						break
					}
				}
			}
			continue
		}

		// Handle special case for $item.property
		if strings.HasPrefix(varPattern, "$item.") {
			propPath := varPattern[6:] // Extract property path after $item.
			extracted := extractPropertyPath(actualItem, propPath)
			if len(extracted) > 0 {
				varValues[varName] = extracted
				continue
			}
		}

		// Regular extraction using pattern
		if strings.HasPrefix(varPattern, "$") {
			// Remove the $ prefix for pattern extraction
			patternWithoutPrefix := strings.TrimPrefix(varPattern, "$")
			extracted := ExtractByPattern(actualItem, patternWithoutPrefix)
			varValues[varName] = extracted
		} else {
			// Use pattern as is
			extracted := ExtractByPattern(actualItem, varPattern)
			varValues[varName] = extracted
		}
	}

	return varValues, nil
}

// extractPropertyPath extracts a property path from an item
func extractPropertyPath(item *yaml.Node, path string) []*yaml.Node {
	if path == "" {
		return []*yaml.Node{item}
	}

	parts := strings.Split(path, ".")
	current := item

	// Navigate through the path
	for _, part := range parts {
		if current.Kind != yaml.MappingNode {
			return nil
		}

		found := false
		for i := 0; i < len(current.Content); i += 2 {
			if current.Content[i].Value == part {
				current = current.Content[i+1]
				found = true
				break
			}
		}

		if !found {
			return nil
		}
	}

	// If the final value is a sequence, return its content
	if current.Kind == yaml.SequenceNode {
		return current.Content
	}

	// Otherwise return the value itself
	return []*yaml.Node{current}
}

// generateCombinations generates all combinations of variable values
func generateCombinations(varValues map[string][]*yaml.Node) []map[string]*yaml.Node {
	if len(varValues) == 0 {
		return []map[string]*yaml.Node{{}}
	}

	// Start with first variable
	var varName string
	var values []*yaml.Node
	for k, v := range varValues {
		varName = k
		values = v
		break
	}

	// Create a copy of remaining variables
	remainingVars := make(map[string][]*yaml.Node)
	for k, v := range varValues {
		if k != varName {
			remainingVars[k] = v
		}
	}

	// Get combinations for remaining variables
	subCombinations := generateCombinations(remainingVars)

	// Combine with values of the current variable
	var result []map[string]*yaml.Node

	// If no values for this variable, skip it entirely
	if len(values) == 0 {
		return subCombinations
	}

	for _, value := range values {
		for _, subCombo := range subCombinations {
			combo := make(map[string]*yaml.Node)
			combo[varName] = value
			for k, v := range subCombo {
				combo[k] = v
			}
			result = append(result, combo)
		}
	}

	return result
}

// processVariableReference processes variable references in a string template
func processVariableReference(templateStr string, context map[string]*yaml.Node) (string, error) {
	result := templateStr
	i := 0

	for i < len(result) {
		if result[i] == '$' && i+1 < len(result) && result[i+1] != '$' {
			// Found a variable reference
			varStart := i

			// Check if it's a simple reference or a path expression
			if i+1 < len(result) && result[i+1] == '{' {
				// Path expression with braces ${var.path}
				i += 2 // Skip past ${
				varEnd := strings.Index(result[i:], "}")
				if varEnd == -1 {
					// No closing brace, treat as literal
					i++
					continue
				}
				varEnd += i
				varPath := strings.TrimSpace(result[i:varEnd])
				i = varEnd + 1
				fullVar := result[varStart:i]

				// Resolve the variable
				replacement, err := resolveVariablePath(varPath, context)
				if err != nil {
					return "", err
				}

				// Replace the variable reference with its value
				if replacement != "" {
					result = strings.Replace(result, fullVar, replacement, 1)
					// Reset i to continue from the start of the replacement
					i = varStart + len(replacement)
				} else {
					// Variable not found, continue
					i = varStart + 1
				}
			} else {
				// Simple reference $var or path expression $var.path
				i++ // Skip past $
				varEnd := i
				for varEnd < len(result) && (isAlphaNumeric(result[varEnd]) || result[varEnd] == '_' || result[varEnd] == '.') {
					varEnd++
				}
				varPath := strings.TrimSpace(result[i:varEnd])
				fullVar := result[varStart:varEnd]
				i = varEnd

				// Resolve the variable
				replacement, err := resolveVariablePath(varPath, context)
				if err != nil {
					return "", err
				}

				// Replace the variable reference with its value
				if replacement != "" {
					result = strings.Replace(result, fullVar, replacement, 1)
					// Reset i to continue from the start of the replacement
					i = varStart + len(replacement)
				} else {
					// Variable not found, continue
					i = varStart + 1
				}
			}
		} else {
			i++
		}
	}

	return result, nil
}

// isAlphaNumeric checks if a character is alphanumeric
func isAlphaNumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// resolveVariablePath resolves a variable path expression against a context
func resolveVariablePath(varPath string, context map[string]*yaml.Node) (string, error) {
	if strings.Contains(varPath, ".") {
		// Handle path expressions like item.name
		parts := strings.Split(varPath, ".")
		if node, ok := context[parts[0]]; ok {
			current := node
			validPath := true
			
			for _, part := range parts[1:] {
				if current.Kind == yaml.MappingNode {
					found := false
					for i := 0; i < len(current.Content); i += 2 {
						if current.Content[i].Value == part {
							current = current.Content[i+1]
							found = true
							break
						}
					}
					if !found {
						validPath = false
						break
					}
				} else {
					validPath = false
					break
				}
			}
			if validPath && current.Kind == yaml.ScalarNode {
				return current.Value, nil
			}
		}
	} else {
		// Simple variable reference
		if node, ok := context[varPath]; ok {
			if node.Kind == yaml.ScalarNode {
				return node.Value, nil
			} else if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
				// For non-scalar nodes, convert to string representation
				// This is a simplification - in a real implementation you might want
				// to handle this differently based on the context
				return fmt.Sprintf("%v", node), nil
			}
		}
	}

	return "", nil
}

// convertStringToNumber converts a string to a number if possible
func convertStringToNumber(valueStr string) interface{} {
	// Try to convert to integer
	if intVal, err := strconv.Atoi(valueStr); err == nil {
		return intVal
	}

	// Try to convert to float
	if floatVal, err := strconv.ParseFloat(valueStr, 64); err == nil {
		return floatVal
	}

	// Return original string if not a number
	return valueStr
}

// applyTemplate applies a template to an item using the provided context
func applyTemplate(item *yaml.Node, template *yaml.Node, context map[string]*yaml.Node) (*yaml.Node, error) {
	if template.Kind == yaml.MappingNode {
		result := node.MakeMapping()
		for i := 0; i < len(template.Content); i += 2 {
			key := template.Content[i]
			value := template.Content[i+1]

			transformedValue, err := applyTemplate(item, value, context)
			if err != nil {
				return nil, err
			}

			// Skip properties with unresolved variables
			if containsUnresolvedVariables(transformedValue) {
				continue
			}

			result.Content = append(result.Content, node.Clone(key), transformedValue)
		}
		return result, nil
	}

	if template.Kind == yaml.SequenceNode {
		result := node.MakeSequence([]string{})
		for _, value := range template.Content {
			transformedValue, err := applyTemplate(item, value, context)
			if err != nil {
				return nil, err
			}

			// Skip items with unresolved variables
			if containsUnresolvedVariables(transformedValue) {
				continue
			}

			result.Content = append(result.Content, transformedValue)
		}
		return result, nil
	}

	if template.Kind == yaml.ScalarNode && strings.Contains(template.Value, "$") {
		// Process string with variable references
		result, err := processVariableReference(template.Value, context)
		if err != nil {
			return nil, err
		}

		// If the result still contains variable references, it means some variables couldn't be resolved
		if strings.Contains(result, "$") && !strings.Contains(result, "$$") {
			// Return a special marker node that will be filtered out later
			return nil, fmt.Errorf("unresolved variable reference in template: %s", template.Value)
		}

		// Convert to number if appropriate (but keep as string node)
		value := convertStringToNumber(result)

		// Create a new scalar node with the same style as the template
		// This preserves quotes in the output
		resultNode := node.MakeScalar(fmt.Sprintf("%v", value))
		resultNode.Style = template.Style

		return resultNode, nil
	}

	// Return as is for non-template values
	return node.Clone(template), nil
}

// groupByAttribute groups items by a specific attribute
func groupByAttribute(items []*yaml.Node, attribute string) *yaml.Node {
	result := node.MakeMapping()

	for _, item := range items {
		if item.Kind != yaml.MappingNode {
			continue
		}

		var keyNode *yaml.Node
		for i := 0; i < len(item.Content); i += 2 {
			if item.Content[i].Value == attribute {
				keyNode = item.Content[i+1]
				break
			}
		}

		if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
			continue
		}

		key := keyNode.Value

		// Check if this key already exists in the result
		var groupNode *yaml.Node
		for i := 0; i < len(result.Content); i += 2 {
			if result.Content[i].Value == key {
				groupNode = result.Content[i+1]
				break
			}
		}

		// If not, create a new group
		if groupNode == nil {
			groupNode = node.MakeSequence([]string{})
			result.Content = append(result.Content, node.MakeScalar(key), groupNode)
		}

		// Add the item to the group
		groupNode.Content = append(groupNode.Content, node.Clone(item))
	}

	return result
}

// containsUnresolvedVariables checks if a node contains any unresolved variable references
func containsUnresolvedVariables(node *yaml.Node) bool {
	if node == nil {
		return false
	}

	if node.Kind == yaml.ScalarNode {
		// Check for $var or ${var} patterns
		return strings.Contains(node.Value, "$") && !strings.Contains(node.Value, "$$")
	}

	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if containsUnresolvedVariables(node.Content[i]) || containsUnresolvedVariables(node.Content[i+1]) {
				return true
			}
		}
	}

	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if containsUnresolvedVariables(child) {
				return true
			}
		}
	}

	return false
}
