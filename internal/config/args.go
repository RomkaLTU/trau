package config

import (
	"fmt"
	"regexp"
	"strconv"
)

// reBareID matches a bare ticket identifier of any tracker prefix (COD-123,
// TMS-456, ENG-7). The pre-config arg scan can't know the configured prefix yet
// — it matches the generic <PREFIX>-<n> shape here and the prefix is validated
// against the loaded config later (see config.ResolvePrefix).
var reBareID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// Options holds the parsed CLI flags. Zero values mean "not set";
// Max is -1 when unset so the config default applies.
type Options struct {
	Parent   string
	Once     bool
	Max      int
	DryRun   bool
	ResetID  string
	NoResume bool
	Status   bool
	Provider string
	Confirm  bool
	Repo     string
	NoTUI    bool
	JSON     bool
	Verbose  bool
	Debug    bool
}

// ParseArgs parses the CLI argument vector. It returns an error on an unknown
// flag or a missing flag value.
func ParseArgs(args []string) (Options, error) {
	o := Options{Max: -1}
	i := 0

	next := func(flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", flag)
		}
		i++
		return args[i], nil
	}
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--parent":
			v, err := next(a)
			if err != nil {
				return o, err
			}
			o.Parent = v
		case a == "--once":
			o.Once = true
		case a == "--max":
			v, err := next(a)
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("--max: %q is not an integer", v)
			}
			o.Max = n
		case a == "--dry-run":
			o.DryRun = true
		case a == "--reset":
			v, err := next(a)
			if err != nil {
				return o, err
			}
			o.ResetID = v
		case a == "--no-resume":
			o.NoResume = true
		case a == "--status":
			o.Status = true
		case a == "--no-tui":
			o.NoTUI = true
		case a == "--json":
			o.JSON = true
		case a == "--verbose":
			o.Verbose = true
		case a == "--debug":
			o.Debug = true
		case a == "--provider":
			v, err := next(a)
			if err != nil {
				return o, err
			}
			o.Provider = v
		case a == "--repo":
			v, err := next(a)
			if err != nil {
				return o, err
			}
			o.Repo = v
		case a == "--yes":
			o.Confirm = true
		case reBareID.MatchString(a):
			o.Parent = a
		default:
			return o, fmt.Errorf("unknown arg: %s", a)
		}
	}

	modes := 0
	for _, on := range []bool{o.Status, o.ResetID != "", o.DryRun} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return o, fmt.Errorf("--status, --reset, and --dry-run are mutually exclusive")
	}

	return o, nil
}
