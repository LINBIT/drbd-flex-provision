/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package request_test

import (
	"testing"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/kubernetes/pkg/api"
	genericapirequest "k8s.io/kubernetes/pkg/genericapiserver/api/request"
	"k8s.io/kubernetes/pkg/types"
)

// TestNamespaceContext validates that a namespace can be get/set on a context object
func TestNamespaceContext(t *testing.T) {
	ctx := genericapirequest.NewDefaultContext()
	result, ok := genericapirequest.NamespaceFrom(ctx)
	if !ok {
		t.Fatalf("Error getting namespace")
	}
	if api.NamespaceDefault != result {
		t.Fatalf("Expected: %s, Actual: %s", api.NamespaceDefault, result)
	}

	ctx = genericapirequest.NewContext()
	result, ok = genericapirequest.NamespaceFrom(ctx)
	if ok {
		t.Fatalf("Should not be ok because there is no namespace on the context")
	}
}

// TestValidNamespace validates that namespace rules are enforced on a resource prior to create or update
func TestValidNamespace(t *testing.T) {
	ctx := genericapirequest.NewDefaultContext()
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	resource := api.ReplicationController{}
	if !api.ValidNamespace(ctx, &resource.ObjectMeta) {
		t.Fatalf("expected success")
	}
	if namespace != resource.Namespace {
		t.Fatalf("expected resource to have the default namespace assigned during validation")
	}
	resource = api.ReplicationController{ObjectMeta: api.ObjectMeta{Namespace: "other"}}
	if api.ValidNamespace(ctx, &resource.ObjectMeta) {
		t.Fatalf("Expected error that resource and context errors do not match because resource has different namespace")
	}
	ctx = genericapirequest.NewContext()
	if api.ValidNamespace(ctx, &resource.ObjectMeta) {
		t.Fatalf("Expected error that resource and context errors do not match since context has no namespace")
	}

	ctx = genericapirequest.NewContext()
	ns := genericapirequest.NamespaceValue(ctx)
	if ns != "" {
		t.Fatalf("Expected the empty string")
	}
}

//TestUserContext validates that a userinfo can be get/set on a context object
func TestUserContext(t *testing.T) {
	ctx := genericapirequest.NewContext()
	_, ok := genericapirequest.UserFrom(ctx)
	if ok {
		t.Fatalf("Should not be ok because there is no user.Info on the context")
	}
	ctx = genericapirequest.WithUser(
		ctx,
		&user.DefaultInfo{
			Name:   "bob",
			UID:    "123",
			Groups: []string{"group1"},
			Extra:  map[string][]string{"foo": {"bar"}},
		},
	)

	result, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		t.Fatalf("Error getting user info")
	}

	expectedName := "bob"
	if result.GetName() != expectedName {
		t.Fatalf("Get user name error, Expected: %s, Actual: %s", expectedName, result.GetName())
	}

	expectedUID := "123"
	if result.GetUID() != expectedUID {
		t.Fatalf("Get UID error, Expected: %s, Actual: %s", expectedUID, result.GetName())
	}

	expectedGroup := "group1"
	actualGroup := result.GetGroups()
	if len(actualGroup) != 1 {
		t.Fatalf("Get user group number error, Expected: 1, Actual: %d", len(actualGroup))
	} else if actualGroup[0] != expectedGroup {
		t.Fatalf("Get user group error, Expected: %s, Actual: %s", expectedGroup, actualGroup[0])
	}

	expectedExtraKey := "foo"
	expectedExtraValue := "bar"
	actualExtra := result.GetExtra()
	if len(actualExtra[expectedExtraKey]) != 1 {
		t.Fatalf("Get user extra map number error, Expected: 1, Actual: %d", len(actualExtra[expectedExtraKey]))
	} else if actualExtra[expectedExtraKey][0] != expectedExtraValue {
		t.Fatalf("Get user extra map value error, Expected: %s, Actual: %s", expectedExtraValue, actualExtra[expectedExtraKey])
	}

}

//TestUIDContext validates that a UID can be get/set on a context object
func TestUIDContext(t *testing.T) {
	ctx := genericapirequest.NewContext()
	_, ok := genericapirequest.UIDFrom(ctx)
	if ok {
		t.Fatalf("Should not be ok because there is no UID on the context")
	}
	ctx = genericapirequest.WithUID(
		ctx,
		types.UID("testUID"),
	)
	_, ok = genericapirequest.UIDFrom(ctx)
	if !ok {
		t.Fatalf("Error getting UID")
	}
}

//TestUserAgentContext validates that a useragent can be get/set on a context object
func TestUserAgentContext(t *testing.T) {
	ctx := genericapirequest.NewContext()
	_, ok := genericapirequest.UserAgentFrom(ctx)
	if ok {
		t.Fatalf("Should not be ok because there is no UserAgent on the context")
	}

	ctx = genericapirequest.WithUserAgent(
		ctx,
		"TestUserAgent",
	)
	result, ok := genericapirequest.UserAgentFrom(ctx)
	if !ok {
		t.Fatalf("Error getting UserAgent")
	}
	expectedResult := "TestUserAgent"
	if result != expectedResult {
		t.Fatalf("Get user agent error, Expected: %s, Actual: %s", expectedResult, result)
	}
}
