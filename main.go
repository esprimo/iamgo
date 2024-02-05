//go:build go1.21

package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"golang.org/x/tools/go/ssa"
)

func usage() {
	fmt.Fprint(os.Stderr, `Find AWS IAM actions needed by a Go project that use the Go AWS SDK v2
	
Usage:
  iamgo [OPTIONS] [PACKAGE]

Options:
`)
	flag.PrintDefaults()

	fmt.Fprint(os.Stderr, `
Examples:
  iamgo .
  iamgo main.go
  iamgo -sdk-calls main.go
  iamgo -why ssm:getparameters .

`)
}

func main() {
	log.SetPrefix("iamgo: ")
	log.SetFlags(0) // don't show timestamp

	var (
		testFlag       = flag.Bool("test", false, "include implicit test packages and executables")
		tagsFlag       = flag.String("tags", "", "comma-separated list of extra build tags (see: go help buildconstraint)")
		reflectionFlag = flag.Bool("reflection", false, "include calls that are only reachable through reflection (false positive prone)")
		sdkcallsFlag   = flag.Bool("sdk-calls", false, "print SDK calls instead of IAM actions")
		whyFlag        = flag.String("why", "", "show a call path to an SDK call that requires a certain permission")
	)

	flag.Usage = usage

	flag.Parse()
	if len(flag.Args()) == 0 {
		usage()
		os.Exit(2)
	}

	if *whyFlag != "" {
		whyFormat := regexp.MustCompile(`^[A-Za-z]+\:[A-Za-z]+$`)
		if !whyFormat.MatchString(*whyFlag) {
			usage()
			log.Fatal("-why value must be an IAM action in format 'service:method', for example '-why ssm:GetParameter'")
		}
	}

	// Load program, create graph etc
	graph := analyze(*testFlag, *tagsFlag)

	// If we just want to list the SDK calls we don't need
	// to load the method->iam mapping
	if !*sdkcallsFlag {
		loadMap()
	}

	// The -why=action flag shows a path of function calls that
	// leads to an AWS SDK call that requires the IAM action
	if *whyFlag != "" {
		// Map AWS IAM action permission to any SDK methods that might need them
		sdkMethods := actionToSDKMethods(*whyFlag)
		if len(sdkMethods) == 0 {
			log.Fatalf("didn't find any SDK method that requires the action %s. Are you sure it exist?", *whyFlag)
		}
		for _, method := range sdkMethods {
			// Based on the SDK method names, find what they might be called in different SDK versions
			for _, fnName := range possibleFunctionNames(method) {
				if path := graph.whyReachable(fnName); path != nil {
					graph.printPath(path)
					return // only print the first match we find
				}
			}
		}
		log.Fatalf("no call path found that requires %s. It might only be reachable via reflection", *whyFlag)
	}

	var sdkMethods []string
	for fn := range graph.reachable {
		if fn.Synthetic != "" {
			continue // ignore synthetic wrappers etc
		}

		// Use origin rather than instantiations
		if orig := fn.Origin(); orig != nil {
			fn = orig
		}

		// Ignore unreachable nested functions
		if fn.Parent() != nil {
			continue
		}

		sdkVersion := sdkVersion(fn)
		if sdkVersion == "" {
			continue // We only care about AWS SDK calls
		}

		// search for a path to determine if it's only reachable
		// through reflection
		if !*reflectionFlag {
			if path := graph.findPath(fn); path == nil { // only reachable through reflection
				continue
			}
		}

		var fnName string
		if sdkVersion == "v1" {
			// All SDK v1 calls has an extra 'Request' suffix
			fnName = strings.TrimSuffix(fn.Name(), "Request")
		} else {
			fnName = fn.Name()
		}

		// The package name is the same as the AWS service name
		sdkMethod := fmt.Sprintf("%s.%s", fn.Pkg.Pkg.Name(), fnName)
		sdkMethods = append(sdkMethods, sdkMethod)
	}

	if len(sdkMethods) == 0 {
		log.Fatalf("found no actiave use of the AWS API via AWS SDK v2")
	}
	if *sdkcallsFlag {
		for _, method := range sdkMethods {
			fmt.Println(method)
		}
		return
	}

	var iamActions []string
	for _, sdkMethod := range sdkMethods {
		iamAction := sdkMethodToAction(sdkMethod)
		if iamAction != "" {
			iamActions = append(iamActions, iamAction)
		}
	}
	if len(iamActions) == 0 {
		// it's uncommon but there are some SDK methods/API calls that doesn't
		// require any IAM permissions to use
		log.Fatalf("found no needed AWS IAM permissions")
	}
	for _, iamAction := range iamActions {
		fmt.Println(iamAction)
	}
}

// possibleFunctionNames takes an SDK method, e.g. "DynamoDB.BatchGetItem",
// and returns a list of strings with the full names the different SDK
// versions use
func possibleFunctionNames(sdkMethod string) []string {
	var fnNames []string
	service := strings.ToLower(strings.Split(sdkMethod, ".")[0])
	method := strings.Split(sdkMethod, ".")[1]

	v1 := fmt.Sprintf("(*github.com/aws/aws-sdk-go/service/%s.%s).%sRequest", // v1 has "Request" suffix
		service,                          // service
		strings.Split(sdkMethod, ".")[0], // Correctly capitalized service name
		method,                           // method
	)
	v2 := fmt.Sprintf("(*github.com/aws/aws-sdk-go-v2/service/%s.Client).%s",
		service, // service
		method,  // method
	)

	return append(fnNames, v1, v2)
}

// sdkVersion determines if a function is a call to AWS SDK v1 or v2.
// Returns an empty string if it's not a call to any of them
func sdkVersion(fn *ssa.Function) string {
	if isAWSSDKv2Call(fn) {
		return "v2"
	}
	if isAWSSDKv1Call(fn) {
		return "v1"
	}

	return ""
}

// isAWSSDKv2Call checks whether a function is an AWS API call via
// AWS SDK v2, based on the name of the package, file and function
func isAWSSDKv2Call(fn *ssa.Function) bool {
	filename := fn.Prog.Fset.Position(fn.Pos()).Filename
	pkgpath := fn.Pkg.Pkg.Path()

	// The SDK method name is in the filename too
	isRelevantFile := strings.HasSuffix(filename, "/api_op_"+fn.Name()+".go")

	return strings.HasPrefix(pkgpath, "github.com/aws/aws-sdk-go-v2/service/") &&
		isRelevantFile
}

func isAWSSDKv1Call(fn *ssa.Function) bool {
	filename := fn.Prog.Fset.Position(fn.Pos()).Filename
	pkgpath := fn.Pkg.Pkg.Path()

	// SDK v1 has no "-vX"
	isRelevantPackage := strings.HasPrefix(pkgpath, "github.com/aws/aws-sdk-go/service/")

	// All SDK v1 API calls happen in api.go
	isRelevantFile := strings.HasSuffix(filename, "/api.go")

	// SDK v1 methods that calls the API are suffixed with "Request"
	// This may have false positives if other functions have "Request" in the name
	// but it seems they either start with 'new' or 'Set' in that case. I'm sure
	// there is a better way to do this but this was quick and seems to work. v1
	// is being deprecated soon too.
	isRelevantFunctionName := strings.HasSuffix(fn.Name(), "Request") &&
		!strings.HasSuffix(fn.Name(), "new") &&
		!strings.HasSuffix(fn.Name(), "Set")

	return isRelevantPackage && isRelevantFile && isRelevantFunctionName
}
