//go:build headless

package main

import "fmt"

func runGUI() {
	fmt.Println(versionText() + " (CLI build). Use -h for usage; build without -tags headless for the GUI.")
}
