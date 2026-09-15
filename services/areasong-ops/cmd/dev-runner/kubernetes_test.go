package main

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(test *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--development-kubectl" {
		code, err := developmentKubectl(os.Args[2:], os.Stdin, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	os.Exit(test.Run())
}
