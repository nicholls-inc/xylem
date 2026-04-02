package main

import (
	"context"
	"os"

	"github.com/nicholls-inc/xylem/cli/internal/dtushim"
)

func main() {
	os.Exit(dtushim.Execute(context.Background(), "gh", os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
