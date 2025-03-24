// The file has functions related to resolving refs in modules
package pkg

import (
	"fmt"
	"strings"

	"github.com/aws-cloudformation/rain/cft"
	"github.com/aws-cloudformation/rain/cft/parse"
	"github.com/aws-cloudformation/rain/cft/visitor"
	"github.com/aws-cloudformation/rain/internal/config"
	"github.com/aws-cloudformation/rain/internal/node"
	"github.com/aws-cloudformation/rain/internal/s11n"
	"gopkg.in/yaml.v3"
)

// Resolve resolves Ref, Sub, and GetAtt in the node,
// using module config Properties, Parameter defaults, and
// other Resource names within the module.
func (module *Module) Resolve(n *yaml.Node) error {

	var err error
	vf := func(v *visitor.Visitor) {
		vn := v.GetYamlNode()
		if vn.Kind != yaml.MappingNode || len(vn.Content) != 2 {
			return
		}
		switch vn.Content[0].Value {
		case string(cft.Ref):
			err = module.ResolveRef(vn)
		case string(cft.Sub):
			err = module.ResolveSub(vn)
		case string(cft.GetAtt):
			err = module.ResolveGetAtt(vn)
		}
		if err != nil {
			v.Stop()
		}
	}
	visitor.NewVisitor(n).Visit(vf)
	return err
}

func (module *Module) ResolveRef(n *yaml.Node) error {

	moduleParams := module.ParametersNode
	templateProps := module.Config.PropertiesNode
	logicalId := module.Config.Name
	moduleResources := module.ResourcesNode

	refFoundInParams := false

	prop := n.Content[1]
	if moduleParams != nil {
		// Find the module parameter that matches the !Ref
		_, param, _ := s11n.GetMapValue(moduleParams, prop.Value)
		if param != nil {
			// We need to get the parameter value from the parent template.
			// Module params are set by the parent template resource properties.
			//
			// For example:
			//
			// The module has this section:
			//
			// Parameters:
			//   Foo:
			//     Type: String
			//
			// And the parent template has this:
			//
			// MyResource:
			//   Type: !Rain::Module "this-module.yaml"
			//   Properties:
			//     Foo: bar
			//
			// Inside the module, we replace !Ref Foo with bar

			// Look for this property name in the parent template
			_, parentVal, _ := s11n.GetMapValue(templateProps, prop.Value)
			if parentVal == nil {
				// Check to see if there is a Default
				_, mParam, _ := s11n.GetMapValue(moduleParams, prop.Value)
				if mParam != nil {
					_, defaultNode, _ := s11n.GetMapValue(mParam, Default)
					if defaultNode != nil {
						parentVal = defaultNode
					}
				}

				// If we didn't find a parent template prop or a default, fail
				if parentVal == nil {
					return fmt.Errorf("did not find %v in parent template Properties",
						prop.Value)
				}
			}

			*n = *parentVal

			refFoundInParams = true
		}
	}
	if !refFoundInParams {
		// Look for a resource in the module
		_, resource, _ := s11n.GetMapValue(moduleResources, prop.Value)
		if resource == nil {
			// If we can't find the Ref, leave it alone and assume it's
			// expected to be in the parent template to be resolved at deploy
			// time. This is sort of cheating. It means you can write a module
			// that has to know about its parent. For example, if you put !Ref
			// Foo in the module, and Foo appears nowhere in the module, we
			// assume it will show up in the parent template. For some use
			// cases, it makes sense to allow this and not consider it an error.
			return nil
		}
		fixedName := rename(logicalId, prop.Value)
		prop.Value = fixedName
	}
	return nil
}

// Resolve a Sub string in a module.
//
// Sub strings can contain several types of variables.
// We leave intrinsics like ${AWS::Region} alone.
// ${Foo} is treated like a Ref to Foo
// ${Foo.Bar} is treated like a GetAtt.
func (module *Module) ResolveSub(n *yaml.Node) error {

	logicalId := module.Config.Name
	moduleResources := module.ResourcesNode

	prop := n.Content[1]
	words, err := parse.ParseSub(prop.Value, true)
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
			var resolved string
			config.Debugf("ResolveSub Ref: %s", word.W)

			// Create a fake node that we can send to Resolve
			fakeNode := node.MakeRef(word.W)
			module.Resolve(fakeNode)
			config.Debugf("fakeNode after Resolve: %s", node.ToSJson(fakeNode))
			switch fakeNode.Kind {
			case yaml.ScalarNode:
				resolved = fakeNode.Value
			case yaml.MappingNode:
				switch fakeNode.Content[0].Value {
				case node.Ref:
					// This might be because nothing changed in the fake node
					resolved = fmt.Sprintf("${%s}", fakeNode.Content[1].Value)
				case node.GetAtt:
					ss := node.SequenceToStrings(fakeNode.Content[1])
					resolved = fmt.Sprintf("${%s}", strings.Join(ss, "."))
				case node.Sub:
					resolved = fakeNode.Content[1].Value
				default:
					return fmt.Errorf("expected Sub reference to be Ref, Sub, or GetAtt: %s", word.W)
				}
			default:
				return fmt.Errorf("invalid Sub reference Kind: %s", word.W)
			}

			// If we didn't change the word, it is either an intrinsic like
			// AWS::Region or a value that is expected to be in the parent
			// template, which is up to the user

			sub += resolved
		case parse.GETATT:
			// All we do here is fix the left part of the GetAtt
			// ${Foo.Bar} becomes ${NameFoo.Bar} where Name is the logicalId
			left, right, found := strings.Cut(word.W, ".")
			if !found {
				return fmt.Errorf("unexpected GetAtt %s", word.W)
			}
			_, resource, _ := s11n.GetMapValue(moduleResources, left)
			if resource != nil {
				left = rename(logicalId, left)
			}
			sub += fmt.Sprintf("${%s.%s}", left, right)
		default:
			return fmt.Errorf("unexpected word type %v for %s", word.T, word.W)
		}
	}

	// Put the sub back if there were any unresolved variables
	var newProp *yaml.Node
	if parse.IsSubNeeded(sub) {
		newProp = node.MakeMapping()
		newProp.Content = append(newProp.Content, node.MakeScalar(string(cft.Sub)))
		newProp.Content = append(newProp.Content, node.MakeScalar(sub))
	} else {
		newProp = node.MakeScalar(sub)
	}

	*n = *newProp

	return nil
}

func (module *Module) ResolveGetAtt(n *yaml.Node) error {
	// A GetAtt somewhere in the module refers to another Resource in the module.
	// Simply prepend the module name.
	prop := n.Content[1]
	fixedName := rename(module.Config.Name, prop.Content[0].Value)
	prop.Content[0].Value = fixedName
	return nil
}

// Rename a resource defined in the module to add the template resource name
func rename(logicalId string, resourceName string) string {
	return logicalId + resourceName
}
