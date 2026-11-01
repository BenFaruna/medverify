// Command eval runs the extraction and assistant evaluations against versioned
// datasets and reports scores.
//
// Phase 10 builds it. Until then the command exists so the layout is complete
// and CI can build every binary; it fails loudly rather than pretending to have
// evaluated anything.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "eval: not implemented yet (phase 10)")
	os.Exit(1)
}
