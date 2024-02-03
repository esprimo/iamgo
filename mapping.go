package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"strings"
)

//go:embed map.json
var bIAMMap []byte

func loadMap() {
	// Load API method -> IAM permission mapping
	err := json.Unmarshal(bIAMMap, &iamMap)
	if err != nil {
		log.Fatal(err)
	}
}

// sdkMethodToAction looks up the IAM action for a given AWS SDK call or returns
// an empty string if there is no match (not all calls require permissions)
func sdkMethodToAction(apiMethod string) string {
	for iamMethodName, iamMethods := range iamMap.SDKMethodIAMMappings {
		if strings.EqualFold(iamMethodName, apiMethod) {
			for _, priv := range iamMethods {
				return priv.Action
			}
		}
	}
	return ""
}

// actionToSDKMethods finds looks up all SDK calls that requires a specific
// IAM action to make. Returns and empty list if no matches are found
func actionToSDKMethods(action string) []string {
	var sdkCalls []string
	for iamMethodName, iamMethods := range iamMap.SDKMethodIAMMappings {
		for _, priv := range iamMethods {
			if strings.EqualFold(priv.Action, action) {
				sdkCalls = append(sdkCalls, iamMethodName)
			}
		}
	}
	return sdkCalls
}

type iamMapMethod struct {
	Action string `json:"action"`
}

type iamMapBase struct {
	SDKMethodIAMMappings map[string][]iamMapMethod `json:"sdk_method_iam_mappings"`
}

var iamMap iamMapBase
