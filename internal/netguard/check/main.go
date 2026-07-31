// Command check refuses a build whose test packages are not all covered by the
// network guard (ADR 0027). It runs as a `make test` prerequisite, before any
// test does, and is never shipped.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

const (
	modulePath = "github.com/RomkaLTU/trau"
	guardPath  = modulePath + "/internal/netguard"
)

// pkg is the slice of `go list -json` this check reads. Deps is transitive, so
// leaked catches a production path to the guard however indirect it is.
type pkg struct {
	ImportPath   string
	TestGoFiles  []string
	XTestGoFiles []string
	TestImports  []string
	XTestImports []string
	Deps         []string
}

func main() {
	pkgs, err := listPackages()
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ netguard check: %v\n", err)
		os.Exit(1)
	}

	missing := uncovered(pkgs)
	unguarded := report(
		missing,
		fmt.Sprintf("✗ netguard: %d test package(s) run without the network guard:", len(missing)),
		"/netguard_test.go",
		fmt.Sprintf("  Each file holds nothing but the package clause of that directory's tests and:\n"+
			"      import _ %q\n"+
			"  Tests must never reach a real external API. See AGENTS.md.", guardPath),
	)
	leaks := report(
		leaked(pkgs),
		"✗ netguard: the guard is test-only, but production code reaches it:",
		"",
		"  Move the import into a _test.go file.",
	)
	if unguarded || leaks {
		os.Exit(1)
	}
}

// report prints a failure block naming one offending package per line, and says
// whether there was anything to report.
func report(pkgs []pkg, header, suffix, hint string) bool {
	if len(pkgs) == 0 {
		return false
	}
	fmt.Fprintln(os.Stderr, header)
	for _, p := range pkgs {
		fmt.Fprintf(os.Stderr, "    %s%s\n", relPath(p), suffix)
	}
	fmt.Fprintln(os.Stderr, hint)
	return true
}

// uncovered names the test-bearing packages whose tests do not import the
// guard. The guard package is exempt: its own test binary contains it already.
func uncovered(pkgs []pkg) []pkg {
	missing := []pkg{}
	for _, p := range pkgs {
		hasTests := len(p.TestGoFiles)+len(p.XTestGoFiles) > 0
		if !hasTests || p.ImportPath == guardPath {
			continue
		}
		if !slices.Contains(p.TestImports, guardPath) && !slices.Contains(p.XTestImports, guardPath) {
			missing = append(missing, p)
		}
	}
	return missing
}

// leaked names the packages that reach the guard from non-test code.
func leaked(pkgs []pkg) []pkg {
	leaks := []pkg{}
	for _, p := range pkgs {
		if p.ImportPath != guardPath && slices.Contains(p.Deps, guardPath) {
			leaks = append(leaks, p)
		}
	}
	return leaks
}

func relPath(p pkg) string {
	return strings.TrimPrefix(p.ImportPath, modulePath+"/")
}

// listPackages asks the toolchain rather than walking the tree: `go list ./...`
// already skips node_modules, dot-directories, web/ and nested modules, on
// every OS (ADR 0023).
func listPackages() ([]pkg, error) {
	var out bytes.Buffer
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}

	pkgs := []pkg{}
	dec := json.NewDecoder(&out)
	for {
		var p pkg
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}
}
