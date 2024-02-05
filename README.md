# iamgo

Find AWS IAM actions used by Go projects!

iamgo builds a representation of a Go project to figure out which AWS SDK (v1 and v2) calls are reachable and maps them to IAM actions (using [IAM Dataset](https://github.com/iann0036/iam-dataset/).)

iamgo can also show a call path of why a certain IAM permission is used using the `-why` flag.

## Install

To build and install this application you must have Go 1.21 or later installed. Try it out with:

```text
go install github.com/esprimo/iamgo@latest
```

## Usage

```text
$ iamgo
Find AWS IAM actions needed by a Go project that use the Go AWS SDK v2

Usage:
  iamgo [OPTIONS] [PACKAGE]

Options:
  -reflection
     include calls that are only reachable through reflection (false positive prone)
  -sdk-calls
     print SDK calls instead of IAM actions
  -tags string
     comma-separated list of extra build tags (see: go help buildconstraint)
  -test
     include implicit test packages and executables
  -why string
     show a call path to an SDK call that requires a certain permission

Examples:
  iamgo .
  iamgo main.go
  iamgo -sdk-calls main.go
  iamgo -why ssm:getparameters .
```

> [!NOTE]
> The target Go code must be buildable with `go build` for iamgo to build a representation of it.

## Examples

This is how it behaves on the AWS provided [IAM example](https://github.com/awsdocs/aws-doc-sdk-examples/blob/main/gov2/iam/cmd/main.go) for AWS SDK v2:

```console
# Setup example project
$ git clone https://github.com/awsdocs/aws-doc-sdk-examples.git
$ cd aws-doc-sdk-examples/gov2/iam/cmd
$ go get .

# List required IAM actions
$ iamgo .
iam:DetachRolePolicy
iam:GetUser
iam:ListAttachedRolePolicies
iam:AttachRolePolicy
iam:CreateUser
sts:AssumeRoleWithWebIdentity
iam:CreatePolicy
s3:ListAllMyBuckets
iam:DeleteUserPolicy
iam:CreateAccessKey
iam:DeleteAccessKey
iam:ListUserPolicies
iam:PutUserPolicy
iam:DeleteUser
iam:CreateRole
sts:AssumeRole
iam:ListAccessKeys
iam:DeletePolicy
iam:DeleteRole

# Show call path why iam:DeleteUser is required
$ iamgo -why iam:DeleteUser .
    github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/cmd.main
    At line 52 a dynamic function call to runAssumeRoleScenario
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/cmd.runAssumeRoleScenario
    Defined at /tmp/aws-doc-sdk-examples/gov2/iam/cmd/main.go:56:6
    At line 62 a static method call to Run
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/scenarios.AssumeRoleScenario.Run
    Defined at /tmp/aws-doc-sdk-examples/gov2/iam/scenarios/scenario_assume_role.go:100:36
    At line 117 a static method call to Cleanup
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/scenarios.AssumeRoleScenario.Cleanup
    Defined at /tmp/aws-doc-sdk-examples/gov2/iam/scenarios/scenario_assume_role.go:274:36
    At line 283 a static method call to DeletePolicy
--> github.com/awsdocs/aws-doc-sdk-examples/gov2/iam/actions.PolicyWrapper.DeletePolicy
    Defined at /tmp/aws-doc-sdk-examples/gov2/iam/actions/policies.go:121:30
    At line 122 a static method call to DeletePolicy
--> github.com/aws/aws-sdk-go-v2/service/iam.Client.DeletePolicy
    Defined at /home/john/go/pkg/mod/github.com/aws/aws-sdk-go-v2/service/iam@v1.28.7/api_op_DeletePolicy.go:31:18
```

## Known issues / limitations

- Only IAM actions are supported (not resources)
- iamgo includes dynamic calls too, which means they may only be reachable based on some condition (e.g. an `if`.) There may be conditionals your code never fulfills to reach a certain call meaning iamgo will print out permissions that are never used
  - You can track down such calls with `-why` and use for example [iamlive](https://github.com/iann0036/iamlive) to dynamically test to see if your code ever reaches that state.
- iamgo has not been tested on nearly enough projects or platforms to be considered reliable so there may be false positives/negatives. Please create a ticket if you find any!
