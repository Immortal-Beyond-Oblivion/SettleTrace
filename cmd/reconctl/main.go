// Command reconctl provides controlled local operator actions.
package main

import (
	"fmt"
	"os"
)

// main prints the supported command surface until store-backed commands are implemented.
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: reconctl verify-chain | help")
		return
	}
	switch os.Args[1] {
	case "help":
		fmt.Println("commands: verify-chain, help")
	case "verify-chain":
		fmt.Println("verify-chain requires configured MySQL store")
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		os.Exit(2)
	}
}
