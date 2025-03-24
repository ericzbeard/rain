package pkg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aws-cloudformation/rain/cft"
	"github.com/aws-cloudformation/rain/cft/parse"
	"github.com/aws-cloudformation/rain/cft/visitor"
	"github.com/aws-cloudformation/rain/internal/config"
	"github.com/aws-cloudformation/rain/internal/node"
	"gopkg.in/yaml.v3"
)

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
		config.Debugf("module %s has no outputs", module.Config.Name)
		return nil
	}

	// Resolve output values first
	for i := 0; i < len(module.OutputsNode.Content); i += 2 {
		outputNode := module.OutputsNode.Content[i+1]
		module.Resolve(outputNode)
	}

	config.Debugf("ProcessOutputs:\n%s", node.YamlStr(module.OutputsNode))

	// Iterate over module outputs
	for outputName, outputVal := range module.Outputs() {
		config.Debugf("processing output %s: %v", outputName, outputVal)

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
				v.Stop()
			case string(cft.GetAtt):
				err = module.OutputGetAtt(outputName, outputVal, n)
				v.Stop()
			default:
				return
			}
		}
		visitor.NewVisitor(module.Parent.Node).Visit(vf)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetArrayIndexFromString extracts an integer array index from a string with
// embedded brackets.  For example, from "Content[1].Arn" it would return 1.
// Returns an error if no valid index is found.
func GetArrayIndexFromString(s string) (int, error) {
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
		return 0, fmt.Errorf("invalid array index in string %s: %w", s, err)
	}

	return num, nil
}

// CheckOutputGetAtt checks to see if a GetAtt string matches an Output.
// Returns nil if it's not a match.
func (module *Module) CheckOutputGetAtt(s string, outputName string, outputVal any) (*yaml.Node, error) {
	tokens := strings.Split(s, ".")
	config.Debugf("CheckOutputGetAtt %s.%s == %s?", module.Config.Name, outputName, s)
	outputValue, err := encodeOutputValue(outputName, outputVal)
	if err != nil {
		return nil, err
	}
	if len(tokens) != 2 {
		return nil, nil
	}

	reffedModuleName := tokens[0]

	// Content[0].Arn
	// If this was a Mapped module, check the original name of the module to see
	// if this is a match. If so, and if the array element matches this module's
	// Map index, return the encoded output value.
	// If we are referencing the entire array using [], then we have to
	// do this later, since we need all Output values.
	if strings.Contains(reffedModuleName, "[") && !strings.Contains(reffedModuleName, "[]") {
		config.Debugf("found [ %+v", module.Config)
		// Look for the reference we saved on the template.
		// This instance of module.Config does not have information about Maps
		if mappedConfig, ok := module.Parent.ModuleMaps[module.Config.Name]; ok {
			config.Debugf("mapped config: %+v", mappedConfig)
			fixedName := strings.Split(reffedModuleName, "[")[0]
			if mappedConfig.OriginalName == fixedName && tokens[1] == outputName {
				idx, err := GetArrayIndexFromString(reffedModuleName)
				if err != nil {
					return nil, err
				}
				if idx == mappedConfig.MapIndex {
					return outputValue, nil
				}
			}
		}
	}

	if reffedModuleName != module.Config.Name {

		return nil, nil
	}
	if tokens[1] != outputName {
		return nil, nil
	}
	return outputValue, nil
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
	config.Debugf("OutputGetAtt %s %s:\n%s", outputName, outputVal, node.YamlStr(n))
	ss := node.SequenceToStrings(n.Content[1])
	o, err := module.CheckOutputGetAtt(strings.Join(ss, "."), outputName, outputVal)
	if err != nil {
		return err
	}
	if o != nil {
		config.Debugf("getatt replacing\n%s\n\nwith\n\n%s", node.ToSJson(n), node.ToSJson(o))
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
		config.Debugf("sub replacing\n%s\n\nwith\n\n%s", node.ToSJson(n), node.ToSJson(subNode))
		*n = *subNode
	}
	return nil
}
