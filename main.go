package main

import (
	"fmt"
	"net/http"
	"os"
)

var Version = "1.1.2"

var globalClient *http.Client

func main() {
	if len(os.Args) > 1 {
		os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
	}
	runGUI()
}

func versionText() string { return fmt.Sprintf("OCImageDownloader %s", Version) }
