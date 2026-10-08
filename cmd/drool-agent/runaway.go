//go:build !droolrunaway

package main

import "context"

// runawayCollector is nil in real builds; see runaway_test_build.go.
var runawayCollector func(ctx context.Context)
