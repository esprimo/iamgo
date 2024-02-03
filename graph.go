package main

import (
	"flag"
	"fmt"
	"log"
	"regexp"
	"slices"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

type graph struct {
	program   *ssa.Program
	roots     []*ssa.Function
	callgraph *callgraph.Graph
	reachable map[*ssa.Function]struct{ AddrTaken bool }
}

type step struct {
	// name of function, including package. If it's an anonymous function
	// the name of the parent is used followed by a dollar sign and the
	// index of the anonymous function
	fullName string
	// name of function
	name string
	// What kind of call was made to this function
	callType string
	// Path to file where funcion is defined
	filename string
	// Line where this function is defined
	line int
	// Column where this function is defined
	column int

	// Line where call to this function happened
	callComingFromLine int
	// Column where call to this function happened
	callComingFromColumn int
	// Name of the file where call to this function happened
	callComingFromFilename string
}

// analyze builds call graph and map reachable functions
func analyze(includeTests bool, buildTags string) *graph {
	mode := packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps
	cfg := &packages.Config{
		BuildFlags: []string{"-tags=" + buildTags},
		Mode:       mode,
		Tests:      includeTests,
	}
	initial, err := packages.Load(cfg, flag.Args()...)
	if err != nil {
		log.Fatalf("failed to load package. Make sure it's bildable with 'go build'\n%v", err)
	}
	if len(initial) == 0 {
		log.Fatalf("no packages")
	}
	if packages.PrintErrors(initial) > 0 {
		log.Fatalf("packages contain errors. Make sure it's buildable with 'go build'")
	}

	prog, pkgs := ssautil.AllPackages(initial, ssa.InstantiateGenerics)
	prog.Build()

	mains := ssautil.MainPackages(pkgs)
	if len(mains) == 0 {
		log.Fatalf("no main packages")
	}

	var roots []*ssa.Function
	for _, main := range mains {
		roots = append(roots, main.Func("init"), main.Func("main"))
	}

	res := rta.Analyze(roots, true)

	return &graph{
		program:   prog,
		roots:     roots,
		callgraph: res.CallGraph,
		reachable: res.Reachable,
	}
}

// whyReachable gives a path of how one reaches a function from any
// main function. Errors if no function is found or a path can't be
// built
func (g *graph) whyReachable(fnName string) []*callgraph.Edge {
	fn := g.findFunc(fnName)
	if fn == nil {
		return nil
	}

	g.callgraph.DeleteSyntheticNodes()

	path := g.findPath(fn)
	if path == nil {
		return nil
	}

	return path
}

// printPath outputs a call path that's intended to be human readable
//
// Output should look like this:
/*
    github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/cmd.main
    At line 52 a dynamic function call to runAssumeRoleScenario
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/cmd.runAssumeRoleScenario
    Defined at /home/john/projects/aws-doc-sdk-examples/gov2/iam/cmd/main.go:56:6
    At line 62 a static method call to Run
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/scenarios.AssumeRoleScenario.Run
    Defined at /home/john/projects/aws-doc-sdk-examples/gov2/iam/scenarios/scenario_assume_role.go:100:36
    At line 114 a static method call to CreateRoleAndPolicies
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/scenarios.AssumeRoleScenario.CreateRoleAndPolicies
    Defined at /home/john/projects/aws-doc-sdk-examples/gov2/iam/scenarios/scenario_assume_role.go:161:36
*/
func (g *graph) printPath(path []*callgraph.Edge) {
	for i, edge := range path {
		if i == 0 { // root/starting point so there is no "called from" etc
			fmt.Printf("    %s\n",
				cleanName(edge.Caller.Func),
			)
		}

		s := g.createStep(edge)
		fmt.Printf("    At line %d a %s to %s\n--> %s\n    Defined at %s:%d:%d\n",
			s.callComingFromLine,
			s.callType,
			s.name,
			s.fullName,
			s.filename,
			s.line,
			s.column,
		)
	}
}

// findFunc looks for a reachable function based on the (clean) name
// of the function. Returns nil if none are found.
func (g *graph) findFunc(fnName string) *ssa.Function {
	for fn := range g.reachable {
		// Only include source named functions (ignore wrappers, instances,
		// anonymous functions etc) becase we need a name to match
		if fn.Synthetic == "" && fn.Object() != nil && fn.String() == fnName {
			return fn
		}
	}
	return nil
}

func (g *graph) createStep(edge *callgraph.Edge) step {
	var outLine int
	var outColumn int
	var outFilename string
	if edge.Site == nil {
		outLine = 0
		outColumn = 0
	} else {
		outLine = g.program.Fset.Position(edge.Site.Pos()).Line
		outColumn = g.program.Fset.Position(edge.Site.Pos()).Column
		outFilename = g.program.Fset.Position(edge.Site.Pos()).Filename
	}
	filename := g.program.Fset.Position(edge.Callee.Func.Pos()).Filename
	if filename == "" {
		filename = "?"
	}

	return step{
		filename:               filename,
		line:                   g.program.Fset.Position(edge.Callee.Func.Pos()).Line,
		column:                 g.program.Fset.Position(edge.Callee.Func.Pos()).Column,
		callComingFromLine:     outLine,
		callComingFromColumn:   outColumn,
		callComingFromFilename: outFilename,
		fullName:               cleanName(edge.Callee.Func),
		name:                   edge.Callee.Func.Name(),
		callType:               edge.Description(),
	}
}

// findPath does a BFS to find the shortest path from any root to the
// target and returns the path. Returns nil if no path is found
func (g *graph) findPath(target *ssa.Function) []*callgraph.Edge {
	var path []*callgraph.Edge
	for _, root := range g.roots {
		path = g.bfs(root, target)
		if path != nil {
			return path
		}
	}

	return nil
}

// bfs does a breadth-first search to find the shortest path from one function
// to another and returns the path. Returns nil if no path is found
func (g *graph) bfs(start *ssa.Function, target *ssa.Function) []*callgraph.Edge {
	root := g.callgraph.Nodes[start]
	visited := make(map[*callgraph.Node]*callgraph.Edge)
	visited[root] = nil
	queue := []*callgraph.Node{root}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current.Func == target { // path found
			path := []*callgraph.Edge{}

			// traverse back up to where we started
			for {
				edge := visited[current]
				if edge == nil { // we've reached the start
					slices.Reverse(path)
					return path
				}
				path = append(path, edge)
				current = edge.Caller
			}
		}

		for _, edge := range current.Out {
			if _, ok := visited[edge.Callee]; !ok {
				visited[edge.Callee] = edge
				queue = append(queue, edge.Callee)
			}
		}
	}

	return nil
}

// filename returns the full path of to file where function fn is defined.
func (g *graph) filename(fn *ssa.Function) string {
	return g.program.Fset.Position(fn.Pos()).Filename
}

// cleanName makes a function name more readable by removing some special0
// characters from the name.
//
// For exmaple
// In:  (*github.com/aws/aws-sdk-go-v2/service/ssm.Client).GetParameter
// Out: github.com/aws/aws-sdk-go-v2/service/ssm.Client.GetParameter
func cleanName(fn *ssa.Function) string {
	return regexp.MustCompile(`[\(\)\*]+`).ReplaceAllString(fn.String(), "")
}
