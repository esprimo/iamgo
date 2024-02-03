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
		sdkMethods := actionToSDKMethods(*whyFlag)
		if len(sdkMethods) == 0 {
			log.Fatalf("didn't find any SDK method that requires the action %s. Are you sure it exist?", *whyFlag)
		}
		for _, method := range sdkMethods {

			fnName := fmt.Sprintf("(*github.com/aws/aws-sdk-go-v2/service/%s.Client).%s",
				strings.ToLower(strings.Split(method, ".")[0]), // service
				strings.Split(method, ".")[1],                  // method
			)

			path := graph.whyReachable(fnName)
			if path != nil {
				graph.printPath(path)
				return // only print the first match we find even though there might be multiple methods
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

		if isAWSSDKv2Call(graph.filename(fn), fn) {
			// search for a path to determine if it's only reachable
			// through reflection
			if !*reflectionFlag {
				path := graph.findPath(fn)
				if path == nil { // only reachable through reflection
					continue
				}
			}

			// the SDK function name is the same as the API method name
			// and the package name is the same as the service
			sdkMethod := fmt.Sprintf("%s.%s", fn.Pkg.Pkg.Name(), fn.Name())
			sdkMethods = append(sdkMethods, sdkMethod)
		}
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

// isAWSSDKv2Call checks whether a function is an AWS API call via
// AWS SDK v2, based on the name of the package, file and function
func isAWSSDKv2Call(filename string, fn *ssa.Function) bool {
	// We only care about AWS SDK v2
	pkgFilter := regexp.MustCompile(`^github.com/aws/aws-sdk-go-v2/service/`)
	pkgpath := fn.Pkg.Pkg.Path()

	// The name of the function that calls the AWS API is in the filename
	filenameFilter := regexp.MustCompile(`/api_op_` + regexp.QuoteMeta(fn.Name()) + `.go$`)

	return pkgFilter.MatchString(pkgpath) && filenameFilter.MatchString(filename)
}
