// Command coworker is the entry point for the coworker runtime binary.
// It defers all command dispatch to the cli package.
package main

import "github.com/chris/coworker/cli"

func main() {
	cli.Execute()
}
