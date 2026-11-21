//go:build mage

package main

import "github.com/magefile/mage/sh"

// Build compiles the binary into bin/
func Build() error {
	return sh.RunV("go", "build", "-o", "bin/wasdarfmeinticket", "./cmd/wasdarfmeinticket")
}

// Test runs vet and all unit tests (offline, recorded fixtures only)
func Test() error {
	if err := sh.RunV("go", "vet", "./..."); err != nil {
		return err
	}
	return sh.RunV("go", "test", "-count=1", "./...")
}

// Run starts the JSON API on :8080
func Run() error {
	return sh.RunV("go", "run", "./cmd/wasdarfmeinticket", "serve")
}
