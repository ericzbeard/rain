package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/aws-cloudformation/rain/lib"
	"github.com/aws-cloudformation/rain/util"

	"github.com/awslabs/aws-cloudformation-template-formatter/parse"
)

func init() {
	Commands["graph"] = Command{
		Type: TEMPLATE,
		Run:  graphCommand,
		Help: "Graph dependencies between resources in a template",
	}
}

func graphCommand(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: rain graph <template filename>")
		os.Exit(1)
	}

	fileName := args[0]

	input, err := parse.ReadFile(fileName)
	if err != nil {
		util.Die(err)
	}

	template := lib.Template(input)

	graph := make(map[string]map[string]bool)

	for _, dep := range template.FindDependencies() {
		from := dep.From.String()
		to := dep.To.String()

		if _, ok := graph[from]; !ok {
			graph[from] = make(map[string]bool)
		}

		graph[from][to] = true
	}

	froms := make([]string, 0)

	for from, _ := range graph {
		froms = append(froms, from)
	}

	sort.Strings(froms)

	for _, from := range froms {
		fmt.Println(from)

		for to, _ := range graph[from] {
			fmt.Println("  ->", to)
		}

		fmt.Println()
	}
}
