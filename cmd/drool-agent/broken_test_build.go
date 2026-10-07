//go:build droolbroken

package main

// brokenForTest makes `run` fail so integration tests can sign and ship a
// release that passes every check but never becomes healthy, to exercise
// the automatic rollback. Only built with -tags droolbroken.
const brokenForTest = "crash"
