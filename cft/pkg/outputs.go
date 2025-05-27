package pkg

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws-cloudformation/rain/cft"
	"github.com/aws-cloudformation/rain/cft/parse"
	"github.com/aws-cloudformation/rain/cft/visitor"
	"github.com/aws-cloudformation/rain/internal/config"
	"github.com/aws-cloudformation/rain/internal/node"
	"github.com/aws-cloudformation/rain/internal/s11n"
	"gopkg.in/yaml.v3"
)

// ProcessOutputArrays looks for array references like Content[].Arn or Content.*.Arn
// and replaces them with the appropriate values.
func ProcessOutputArrays(t *cft.Template) error {
	if t == nil {
		return errors.New("t is nil")
	}

	var err error

	vf := func(v *visitor.Visitor) {
		// Look for patterns like:
		// - A[*].B (wildcard)
		// - A.*.B (dot notation wildcard)
		// - A[key].B (key access)
		// - A.key.B (dot notation key access)

		n := v.GetYamlNode()
		if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
			return
		}

		if n.Content[0].Value != string(cft.GetAtt) {
			return
		}

		getatt := n.Content[1]
		if getatt.Kind != yaml.SequenceNode || len(getatt.Content) != 2 {
			err = fmt.Errorf("invalid getatt: %s", node.YamlStr(getatt))
			v.Stop()
			return
		}

		// Check for wildcard notation: Module[*].Output or Module.*.Output
		moduleRef := getatt.Content[0].Value
		outputName := getatt.Content[1].Value

		// Convert dot notation to bracket notation for consistency
		if strings.Contains(moduleRef, ".") && !strings.Contains(moduleRef, "[") {
			parts := strings.SplitN(moduleRef, ".", 2)
			if len(parts) == 2 {
				if parts[1] == "*" {
					moduleRef = parts[0] + "[*]"
				} else {
					// This could be a key reference like Module.key
					// Check if it's a ForEach module
					if _, ok := t.ModuleForEachNames[parts[0]]; ok {
						moduleRef = parts[0] + "[" + parts[1] + "]"
					}
				}
				getatt.Content[0].Value = moduleRef
			}
		}

		// Process wildcard notation
		if strings.Contains(moduleRef, "[*]") {
			moduleName := strings.Replace(moduleRef, "[*]", "", 1)
			names, ok := t.ModuleForEachNames[moduleName]
			if !ok {
				err = fmt.Errorf("module names not found for %s", moduleName)
				v.Stop()
				return
			}

			items := make([]*yaml.Node, 0)
			for _, name := range names {
				outputs, ok := t.ModuleOutputs[name]
				if !ok {
					err = fmt.Errorf("%s not found in ModuleOutputs", name)
					v.Stop()
					return
				}
				_, o, _ := s11n.GetMapValue(outputs, outputName)
				if o == nil {
					err = fmt.Errorf("%s not found in Outputs for %s", outputName, name)
					v.Stop()
					return
				}
				_, val, _ := s11n.GetMapValue(o, "Value")
				if val == nil {
					err = fmt.Errorf("module output %s.%s missing Value", name, outputName)
					v.Stop()
					return
				}
				items = append(items, val)
			}
			seq := &yaml.Node{Kind: yaml.SequenceNode, Content: items}
			*n = *seq
			return
		}

		// Process key access notation: Module[key].Output
		if strings.Contains(moduleRef, "[") && strings.Contains(moduleRef, "]") {
			moduleName, key, hasKey := extractModuleNameAndKey(moduleRef)
			if !hasKey {
				return
			}

			// Check if this is a ForEach module
			if foreachConfig, ok := t.ModuleForEach[moduleName+key]; ok {
				// This is a key-based access to a ForEach module
				outputs, ok := t.ModuleOutputs[foreachConfig.Name]
				if !ok {
					err = fmt.Errorf("%s not found in ModuleOutputs", foreachConfig.Name)
					v.Stop()
					return
				}
				_, o, _ := s11n.GetMapValue(outputs, outputName)
				if o == nil {
					err = fmt.Errorf("%s not found in Outputs for %s", outputName, foreachConfig.Name)
					v.Stop()
					return
				}
				_, val, _ := s11n.GetMapValue(o, "Value")
				if val == nil {
					err = fmt.Errorf("module output %s.%s missing Value", foreachConfig.Name, outputName)
					v.Stop()
					return
				}
				*n = *val
				return
			}

			// Try numeric index
			idx, idxErr := strconv.Atoi(key)
			if idxErr == nil {
				// This is a numeric index access
				name := fmt.Sprintf("%s%d", moduleName, idx)
				outputs, ok := t.ModuleOutputs[name]
				if !ok {
					err = fmt.Errorf("%s not found in ModuleOutputs", name)
					v.Stop()
					return
				}
				_, o, _ := s11n.GetMapValue(outputs, outputName)
				if o == nil {
					err = fmt.Errorf("%s not found in Outputs for %s", outputName, name)
					v.Stop()
					return
				}
				_, val, _ := s11n.GetMapValue(o, "Value")
				if val == nil {
					err = fmt.Errorf("module output %s.%s missing Value", name, outputName)
					v.Stop()
					return
				}
				*n = *val
				return
			}
		}
	}

	visitor.NewVisitor(t.Node).Visit(vf)

	return err
}

// extractModuleNameAndKey extracts the module name and key from a string like "ModuleName[Key]"
func extractModuleNameAndKey(s string) (string, string, bool) {
	openBracket := strings.Index(s, "[")
	closeBracket := strings.Index(s, "]")

	if openBracket == -1 || closeBracket == -1 || closeBracket <= openBracket {
		return s, "", false
	}

	moduleName := s[:openBracket]
	key := s[openBracket+1 : closeBracket]

	return moduleName, key, true
}

// processModuleOutputs looks for any references in the parent
// template to the module's outputs and replaces them.
func (module *Module) ProcessOutputs() error {

	// Visit each node in the parent template. If we see a Ref, Sub, or
	// GetAtt that points to one of this module's output values,
	// Replace the reference with that value.

	if module == nil {
		return fmt.Errorf("module is nil")
	}

	if module.Config == nil {
		return fmt.Errorf("module config is nil")
	}

	if module.OutputsNode == nil {
		return nil
	}

	// Resolve output values first
	for i := 0; i < len(module.OutputsNode.Content); i += 2 {
		outputNode := module.OutputsNode.Content[i+1]
		module.Resolve(outputNode)
	}

	t := module.ParentTemplate

	// Store the Outputs node on the template for later use
	if t.ModuleOutputs == nil {
		t.ModuleOutputs = make(map[string]*yaml.Node)
	}
	t.ModuleOutputs[module.Config.Name] = module.OutputsNode

	// Iterate over module outputs
	for outputName, outputVal := range module.Outputs() {

		var err error

		// Use a visitor function to find Fn::Sub or Fn::GetAtt that points to
		// this output. Refs can't point to module outputs since you need
		// ModuleName.OutputName
		vf := func(v *visitor.Visitor) {
			n := v.GetYamlNode()
			if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
				return
			}
			switch n.Content[0].Value {
			case string(cft.Sub):
				err = module.OutputSub(outputName, outputVal, n)
				if err != nil {
					v.Stop()
					return
				}
			case string(cft.GetAtt):
				err = module.OutputGetAtt(outputName, outputVal, n)
				if err != nil {
					v.Stop()
					return
				}
			default:
				return
			}
		}
		visitor.NewVisitor(module.ParentTemplate.Node).Visit(vf)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetArrayIndexFromString extracts an integer array index from a string with
// embedded brackets.  For example, from "Content[1].Arn" it would return 1.
// Returns an error if no valid index is found.
func (module *Module) GetArrayIndexFromString(s string) (int, error) {
	// Find the opening bracket position
	start := strings.Index(s, "[")
	if start == -1 {
		return 0, fmt.Errorf("no opening bracket found in string: %s", s)
	}

	// Find the closing bracket position
	end := strings.Index(s[start:], "]")
	if end == -1 {
		return 0, fmt.Errorf("no closing bracket found in string: %s", s)
	}

	// Extract the number between brackets
	numStr := s[start+1 : start+end]

	// Convert to integer
	num, err := strconv.Atoi(numStr)
	if err != nil {
		// This might be a foreach key instead of an index
		foreaches := module.ParentTemplate.ModuleForEach
		if foreaches != nil {
			config.Debugf("foreaches: %v", foreaches)
			name := s[0:start] + numStr
			config.Debugf("Looking for ModuleForEach[%s]", name)
			if cfg, ok := foreaches[name]; ok {
				return cfg.ForEachIndex, nil
			}
		}
		return 0, fmt.Errorf("invalid array index in string %s: %w", s, err)
	}

	return num, nil
}

// CheckOutputGetAtt checks to see if a GetAtt string matches an Output.
// Returns nil if it's not a match.
func (module *Module) CheckOutputGetAtt(s string, outputName string, outputVal any) (*yaml.Node, error) {
	// Handle dot notation (Module.Output)
	tokens := strings.Split(s, ".")
	outputValue, err := encodeOutputValue(outputName, outputVal)
	if err != nil {
		return nil, err
	}

	// Handle different notation formats
	if len(tokens) == 2 {
		// Standard notation: ModuleName.OutputName
		reffedModuleName := tokens[0]

		// Handle bracket notation: ModuleName[key] or ModuleName[index]
		if strings.Contains(reffedModuleName, "[") {
			moduleName, key, hasKey := extractModuleNameAndKey(reffedModuleName)

			// Handle wildcard notation: ModuleName[*]
			if hasKey && key == "*" {
				// This is handled by ProcessOutputArrays
				return nil, nil
			}

			// Handle key or index notation: ModuleName[key] or ModuleName[index]
			if hasKey {
				// If this is a ForEach module, check if this instance matches
				if foreachConfig, ok := module.ParentTemplate.ModuleForEach[module.Config.Name]; ok {
					if foreachConfig.OriginalName == moduleName && tokens[1] == outputName {
						// Check if the key matches this module's key
						if key == foreachConfig.ForEachKey || key == strconv.Itoa(foreachConfig.ForEachIndex) {
							return outputValue, nil
						}
					}
				}
				return nil, nil
			}
		}

		// Standard module output reference
		if reffedModuleName != module.Config.Name {
			return nil, nil
		}
		if tokens[1] != outputName {
			return nil, nil
		}
		return outputValue, nil
	} else if len(tokens) == 3 {
		// Dot notation for key access: ModuleName.key.OutputName
		reffedModuleName := tokens[0]
		key := tokens[1]

		// Check if this is a ForEach module
		if foreachConfig, ok := module.ParentTemplate.ModuleForEach[module.Config.Name]; ok {
			if foreachConfig.OriginalName == reffedModuleName && tokens[2] == outputName {
				// Check if the key matches this module's key
				if key == foreachConfig.ForEachKey || key == strconv.Itoa(foreachConfig.ForEachIndex) {
					return outputValue, nil
				}
			}
		}
	}

	return nil, nil
}

// Convert an output value back to a Node.
// Earlier, we converted nodes to maps to make them a little easier to use.
// This also has the benefit of doing a deep copy to avoid
// accidentally referring to the same object repeatedly.
func encodeOutputValue(outputName string, outputVal any) (*yaml.Node, error) {
	var outputNode yaml.Node
	outputValMap, ok := outputVal.(map[string]any)
	// TODO - It could be a plain string.. though this would be rare
	// Output:
	//   Value: foo
	if !ok {
		return nil, fmt.Errorf("output value %s is not a map", outputName)
	}
	val, ok := outputValMap["Value"]
	if !ok {
		return nil, fmt.Errorf("output value %s does not have a Value", outputName)
	}
	err := outputNode.Encode(val)
	if err != nil {
		return nil, err
	}
	return &outputNode, nil
}

// A GetAtt to a module output.
// For example, !GetAtt A.B, where A is a module name, and B is a module output.
// B could be anything, a Scalar or an Object.
// It is also possible to reference module Maps (duplicate copies of a module),
// by using array brackets in the name, like A[0].B to reference a single
// instance of the module's Output, or A[].B, to get all module Outputs that
// have that name and return a Sequence with all of the Output values.
func (module *Module) OutputGetAtt(outputName string, outputVal any, n *yaml.Node) error {
	if n.Content[1].Kind != yaml.SequenceNode {
		return fmt.Errorf("expected GetAtt in %s to be a sequence: %s",
			module.Config.Name, node.ToSJson(n))
	}
	ss := node.SequenceToStrings(n.Content[1])
	o, err := module.CheckOutputGetAtt(strings.Join(ss, "."), outputName, outputVal)
	if err != nil {
		return err
	}
	if o != nil {
		*n = *o
	}
	return nil
}

// OutputSub checks a Sub to see if it refers to a module Output.
// A Sub string can refer to an output scalar value.
// The reference needs to be like a GetAtt.
// For example, !Sub ${A.B} refers to module A, output B.
func (module *Module) OutputSub(outputName string, outputVal any, n *yaml.Node) error {
	s := n.Content[1].Value
	words, err := parse.ParseSub(s, true)
	if err != nil {
		return err
	}
	sub := ""
	for _, word := range words {
		switch word.T {
		case parse.STR:
			sub += word.W
		case parse.AWS:
			sub += "${AWS::" + word.W + "}"
		case parse.REF:
			sub += "${" + word.W + "}"
		case parse.GETATT:
			outputValue, err := module.CheckOutputGetAtt(word.W, outputName, outputVal)
			if err != nil {
				return err
			}
			if outputValue == nil {
				sub += "${" + word.W + "}"
			} else {
				if outputValue.Kind == yaml.MappingNode {
					// Prepend the module name
					v := outputValue.Content[1]
					switch outputValue.Content[0].Value {
					case string(cft.Sub):
						sub += "${" + v.Value + "}"
					case string(cft.GetAtt):
						ss := node.SequenceToStrings(v)
						joined := strings.Join(ss, ".")
						sub += "${" + joined + "}"
					case string(cft.Ref):
						sub += "${" + v.Value + "}"
					}
				} else if outputValue.Kind == yaml.ScalarNode {
					sub += outputValue.Value
				}
			}
		}
	}

	var subNode *yaml.Node
	if parse.IsSubNeeded(sub) {
		subNode = node.MakeMapping()
		subNode.Content = append(subNode.Content, node.MakeScalar(string(cft.Sub)))
		subNode.Content = append(subNode.Content, node.MakeScalar(sub))
	} else {
		subNode = node.MakeScalar(sub)
	}
	if sub != s {
		*n = *subNode
	}
	return nil
}
