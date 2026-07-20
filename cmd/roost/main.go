// Command roost turns a list of local application folders into live,
// HTTPS-accessible apps on the developer's own domain.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "roost:", err)
		os.Exit(1)
	}
}
