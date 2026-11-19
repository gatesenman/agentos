package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentos/agentos/internal/env/providers/hyperv"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: isoextract <iso> <path-in-iso> <dest>")
		os.Exit(2)
	}
	iso, name, dest := os.Args[1], os.Args[2], os.Args[3]
	name = strings.TrimPrefix(name, "/")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fatal(err)
	}
	if err := hyperv.IsoExtractFile(iso, name, dest); err != nil {
		fatal(err)
	}
	st, _ := os.Stat(dest)
	fmt.Printf("OK %s -> %s (%d bytes)\n", name, dest, st.Size())
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
