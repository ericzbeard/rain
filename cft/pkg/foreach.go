// ForEach processing for modules
package pkg

import (
	"fmt"
	"strings"

	"github.com/aws-cloudformation/rain/cft"
	"github.com/aws-cloudformation/rain/cft/parse"
	"github.com/aws-cloudformation/rain/cft/visitor"
	"github.com/aws-cloudformation/rain/internal/node"
	"github.com/aws-cloudformation/rain/internal/s11n"
	"gopkg.in/yaml.v3"
)

const (
	MI = "$Index"
	MV = "$Identifier"
)

func replaceForEachStr(s string, index int, key string, identifier string) string {
	s = strings.Replace(s, "${"+MI+"}", fmt.Sprintf("%d", index), -1)
	s = strings.Replace(s, "&{"+MI+"}", fmt.Sprintf("%d", index), -1)
	s = strings.Replace(s, MI, fmt.Sprintf("%d", index), -1)
	s = strings.Replace(s, "${"+MV+"}", key, -1)
	s = strings.Replace(s, "&{"+MV+"}", key, -1)
	s = strings.Replace(s, MV, key, -1)

	if identifier != "" {
		s = cft.ReplaceIdentifier(s, key, identifier)
	}

	return s
}

// foreachPlaceholders looks for Index and Identifier and replaces them
func foreachPlaceholders(n *yaml.Node, index int, key string, identifier string) {

	vf := func(v *visitor.Visitor) {
		yamlNode := v.GetYamlNode()
		if yamlNode.Kind == yaml.MappingNode {
			content := yamlNode.Content
			if len(content) == 2 {
				switch content[0].Value {
				case string(cft.Sub):
					r := replaceForEachStr(content[1].Value,
						index, key, identifier)
					if parse.IsSubNeeded(r) {
						yamlNode.Value = r
					} else {
						*yamlNode = *node.MakeScalar(r)
					}
				case string(cft.GetAtt):
					for _, getatt := range content[1].Content {
						getatt.Value = replaceForEachStr(getatt.Value, index, key,
							identifier)
					}
				}
			}
		} else if yamlNode.Kind == yaml.ScalarNode {
			yamlNode.Value = replaceForEachStr(yamlNode.Value, index, key,
				identifier)
		}
	}

	visitor.NewVisitor(n).Visit(vf)
}

// getForEachKeys gets the CSV key values from either a hard-coded
// string or from a Ref.
func getForEachKeys(moduleConfig *cft.ModuleConfig, t *cft.Template,
	parentModule *Module) ([]string, error) {

	// The foreach is either a CSV or a Ref to a CSV that we can fully
	// resolve
	foreachJson := node.ToSJson(moduleConfig.ForEach)
	var keys []string
	if moduleConfig.ForEach.Kind == yaml.ScalarNode {
		keys = node.StringsFromNode(moduleConfig.ForEach)
	} else if moduleConfig.ForEach.Kind == yaml.SequenceNode {
		keys = node.StringsFromNode(moduleConfig.ForEach)
	} else if moduleConfig.ForEach.Kind == yaml.MappingNode {
		if cft.IsRef(moduleConfig.ForEach) {
			r := moduleConfig.ForEach.Content[1].Value
			// Look in the parent templates Parameters for the Ref
			if !t.HasSection(cft.Parameters) {
				msg := "module ForEach Ref no Parameters: %s"
				return nil, fmt.Errorf(msg, foreachJson)
			}
			params, _ := t.GetSection(cft.Parameters)
			_, keysNode, _ := s11n.GetMapValue(params, r)
			if keysNode == nil {
				msg := "expected module ForEach Ref to a Parameter: %s"
				return nil, fmt.Errorf(msg, foreachJson)
			}

			// Look at the parent module Properties
			// TODO: Will this work in nested modules?
			// Have those props been resolved?
			if parentModule != nil {
				if parentVal, ok := parentModule.Config.Properties()[r]; ok {
					csv, ok := parentVal.(string)
					if ok {
						keys = strings.Split(csv, ",")
					} else {
						msg := "expected ForEach keys to be a CSV: %v"
						return nil, fmt.Errorf(msg, parentVal)
					}
				}
			}

			if len(keys) == 0 {
				_, d, _ := s11n.GetMapValue(keysNode, "Default")
				if d == nil {
					msg := "expected module ForEach Ref to a Default: %s"
					return nil, fmt.Errorf(msg, foreachJson)
				}
				keys = node.StringsFromNode(d)
			}
		} else {
			msg := "expected module ForEach to be a Ref: %s"
			return nil, fmt.Errorf(msg, foreachJson)
		}
	} else {
		return nil, fmt.Errorf("unexpected module ForEach Kind: %s", foreachJson)
	}

	return keys, nil
}

// processForEach duplicates module configuration in the template for
// each value in a CSV.
func processForEach(originalContent []*yaml.Node,
	t *cft.Template, parentModule *Module) ([]*yaml.Node, error) {

	content := make([]*yaml.Node, 0)

	// Process ForEach, which duplicate the module for each element in a list
	for i := 0; i < len(originalContent); i += 2 {
		name := originalContent[i].Value
		moduleConfig, err := t.ParseModuleConfig(name, originalContent[i+1])
		if err != nil {
			return nil, err
		}

		if moduleConfig.ForEach == nil {
			content = append(content, originalContent[i])
			content = append(content, originalContent[i+1])
			continue
		}

		keys, err := getForEachKeys(moduleConfig, t, parentModule)
		if err != nil {
			return nil, err
		}

		if len(keys) < 1 {
			msg := "expected module ForEach to have items: %s"
			foreachErr := fmt.Errorf(msg, node.YamlStr(moduleConfig.Node))
			return nil, foreachErr
		}

		// Duplicate the config
		for i, key := range keys {
			foreachName := fmt.Sprintf("%s%d", moduleConfig.Name, i)

			if moduleConfig.FnForEach != nil &&
				moduleConfig.FnForEach.OutputKeyHasIdentifier() {
				// The OutputKey is something like A${Identifier},
				// which means we use the key instead of the
				// array index to create the logical id
				foreachName = fmt.Sprintf("%s%s", moduleConfig.Name, key)
			}

			copiedNode := node.Clone(moduleConfig.Node)
			node.RemoveFromMap(copiedNode, ForEach)
			copiedConfig, err := t.ParseModuleConfig(foreachName, copiedNode)
			if err != nil {
				return nil, err
			}

			// These values won't go into the YAML but we'll store them for
			// later

			copiedConfig.OriginalName = moduleConfig.Name
			copiedConfig.IsForEachCopy = true
			copiedConfig.ForEachIndex = i
			copiedConfig.ForEachKey = key

			// Add a reference to the template so we can find it later for
			// Outputs

			t.AddForEachModule(copiedConfig)

			// Replace $Index and $Identifier
			identifier := ""
			if moduleConfig.FnForEach != nil {
				identifier = moduleConfig.FnForEach.Identifier
			}
			foreachPlaceholders(copiedNode, i, key, identifier)

			content = append(content, node.MakeScalar(foreachName))
			content = append(content, copiedNode)
		}
	}
	return content, nil
}
