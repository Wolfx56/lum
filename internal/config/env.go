package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LookupFunc resolves an environment variable (same contract as os.LookupEnv).
type LookupFunc func(name string) (string, bool)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// MissingRef is a ${VAR} reference that could not be resolved.
type MissingRef struct {
	Var  string // variable name
	Path string // config path that references it, e.g. apps[0].current.token
}

// MissingEnvError lists every unresolved ${VAR}; it blocks startup (spec §4).
type MissingEnvError struct {
	Refs []MissingRef
}

// Vars returns the sorted, de-duplicated names of the missing variables.
func (e *MissingEnvError) Vars() []string {
	seen := map[string]bool{}
	var vars []string
	for _, r := range e.Refs {
		if !seen[r.Var] {
			seen[r.Var] = true
			vars = append(vars, r.Var)
		}
	}
	sort.Strings(vars)
	return vars
}

func (e *MissingEnvError) Error() string {
	paths := map[string][]string{}
	for _, r := range e.Refs {
		paths[r.Var] = append(paths[r.Var], r.Path)
	}
	parts := make([]string, 0, len(paths))
	for _, v := range e.Vars() {
		parts = append(parts, fmt.Sprintf("%s (%s)", v, strings.Join(paths[v], ", ")))
	}
	return "missing environment variables: " + strings.Join(parts, ", ")
}

// substituteEnv replaces ${VAR} in every string field of v (a pointer to a struct),
// recording unresolved references. Only string values are substituted, after YAML
// parsing, so a variable value can never alter the YAML structure.
func substituteEnv(v any, lookup LookupFunc) error {
	var missing []MissingRef
	walkStrings(reflect.ValueOf(v), "", func(path string, s reflect.Value) {
		s.SetString(envRef.ReplaceAllStringFunc(s.String(), func(ref string) string {
			name := envRef.FindStringSubmatch(ref)[1]
			val, ok := lookup(name)
			if !ok {
				missing = append(missing, MissingRef{Var: name, Path: path})
				return ref
			}
			return val
		}))
	})
	if len(missing) > 0 {
		return &MissingEnvError{Refs: missing}
	}
	return nil
}

// walkStrings calls fn for every settable string-kind value reachable from v,
// with its config path built from the yaml tags.
func walkStrings(v reflect.Value, path string, fn func(path string, s reflect.Value)) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			walkStrings(v.Elem(), path, fn)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if name == "" || name == "-" {
				name = strings.ToLower(f.Name)
			}
			walkStrings(v.Field(i), joinPath(path, name), fn)
		}
	case reflect.Slice:
		for i := range v.Len() {
			walkStrings(v.Index(i), path+"["+strconv.Itoa(i)+"]", fn)
		}
	case reflect.String:
		if v.CanSet() {
			fn(path, v)
		}
	}
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// readEnvFile parses a .env file: KEY=VALUE lines, optional "export " prefix,
// # comments, optional single or double quotes. A missing file is not an error.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		vars[key] = val
	}
	return vars, sc.Err()
}

// envLookup resolves from the process environment first, then from the .env values.
func envLookup(primary LookupFunc, file map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		if v, ok := primary(name); ok {
			return v, true
		}
		v, ok := file[name]
		return v, ok
	}
}
