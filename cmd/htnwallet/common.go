package main

import (
	"fmt"
	"os"
	"time"
)

const daemonTimeout = 10 * time.Minute

func printErrorAndExit(err error) {
	fmt.Fprintf(os.Stderr, "%s\n", err)
	os.Exit(1)
}
