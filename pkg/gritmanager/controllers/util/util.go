// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package util

import (
	"context"
	"fmt"
	"hash/fnv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/dump"
)

type controllerNameKeyType struct{}

var (
	controllerNameKey = controllerNameKeyType{}
)

func WithControllerName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, controllerNameKey, name)
}

func ComputeHash(spec *corev1.PodSpec) string {
	hasher := fnv.New32a()
	hasher.Reset()
	fmt.Fprintf(hasher, "%v", dump.ForHash(spec))
	return fmt.Sprint(hasher.Sum32())
}
