package config

// Port of pi's packages/coding-agent/src/core/resolve-config-value.ts.
// Portions Copyright (c) 2025 Mario Zechner, MIT License; see
// THIRD_PARTY_NOTICES.
//
// Config values (API keys, header values) are one of:
//   - "!command": run the command, use its trimmed stdout (cached)
//   - a template: "$NAME" or "${NAME}" are environment variables, "$$"
//     is a literal "$" and "$!" a literal "!"; everything else is literal

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

var (
	envVarNameRE       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envVarNamePrefixRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	commandCacheMu     sync.Mutex
	commandResultCache = map[string]*string{}
)

type templatePart struct {
	env   bool
	value string // literal text, or the variable name
}

func parseConfigValueTemplate(config string) []templatePart {
	var parts []templatePart
	literal := func(v string) {
		if v == "" {
			return
		}
		if n := len(parts); n > 0 && !parts[n-1].env {
			parts[n-1].value += v
			return
		}
		parts = append(parts, templatePart{value: v})
	}
	i := 0
	for i < len(config) {
		d := strings.IndexByte(config[i:], '$')
		if d < 0 {
			literal(config[i:])
			break
		}
		d += i
		literal(config[i:d])
		next := byte(0)
		if d+1 < len(config) {
			next = config[d+1]
		}
		switch {
		case next == '$' || next == '!':
			literal(string(next))
			i = d + 2
		case next == '{':
			end := strings.IndexByte(config[d+2:], '}')
			if end < 0 {
				literal("$")
				i = d + 1
				continue
			}
			end += d + 2
			name := config[d+2 : end]
			if envVarNameRE.MatchString(name) {
				parts = append(parts, templatePart{env: true, value: name})
			} else {
				literal(config[d : end+1])
			}
			i = end + 1
		default:
			if m := envVarNamePrefixRE.FindString(config[d+1:]); m != "" {
				parts = append(parts, templatePart{env: true, value: m})
				i = d + 1 + len(m)
			} else {
				literal("$")
				i = d + 1
			}
		}
	}
	return parts
}

func isCommandConfigValue(config string) bool { return strings.HasPrefix(config, "!") }

func resolveEnvConfigValue(name string, env map[string]string) (string, bool) {
	if v := env[name]; v != "" {
		return v, true
	}
	if v := os.Getenv(name); v != "" {
		return v, true
	}
	return "", false
}

// ConfigValueEnvVarNames lists the variables a template refers to.
func ConfigValueEnvVarNames(config string) []string {
	if isCommandConfigValue(config) {
		return nil
	}
	var names []string
	for _, p := range parseConfigValueTemplate(config) {
		if p.env && !contains(names, p.value) {
			names = append(names, p.value)
		}
	}
	return names
}

func contains(xs []string, x string) bool {
	return slices.Contains(xs, x)
}

// ResolveConfigValue resolves a key or header value; ok is false when a
// referenced variable is unset or the command failed.
func ResolveConfigValue(config string, env map[string]string) (string, bool) {
	if isCommandConfigValue(config) {
		return executeCommand(config, true)
	}
	return resolveTemplate(config, env)
}

// ResolveConfigValueUncached is ResolveConfigValue without the command
// cache (pi uses it for headers).
func ResolveConfigValueUncached(config string, env map[string]string) (string, bool) {
	if isCommandConfigValue(config) {
		return executeCommand(config, false)
	}
	return resolveTemplate(config, env)
}

func resolveTemplate(config string, env map[string]string) (string, bool) {
	var b strings.Builder
	for _, p := range parseConfigValueTemplate(config) {
		if !p.env {
			b.WriteString(p.value)
			continue
		}
		v, ok := resolveEnvConfigValue(p.value, env)
		if !ok {
			return "", false
		}
		b.WriteString(v)
	}
	return b.String(), true
}

func executeCommand(config string, cached bool) (string, bool) {
	if cached {
		commandCacheMu.Lock()
		if v, ok := commandResultCache[config]; ok {
			commandCacheMu.Unlock()
			if v == nil {
				return "", false
			}
			return *v, true
		}
		commandCacheMu.Unlock()
	}
	command := config[1:]
	sh := shell.Shell{Kind: shell.Sh, Path: "sh"}
	if runtime.GOOS == "windows" {
		sh = shell.Shell{Kind: shell.Cmd, Path: "cmd.exe"}
	}
	cmd := sh.Command(context.Background(), command)
	var out []byte
	done := make(chan error, 1)
	go func() {
		var err error
		out, err = cmd.Output()
		done <- err
	}()
	var result *string
	select {
	case err := <-done:
		if s := strings.TrimSpace(string(out)); err == nil && s != "" {
			result = &s
		}
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
	if cached {
		commandCacheMu.Lock()
		commandResultCache[config] = result
		commandCacheMu.Unlock()
	}
	if result == nil {
		return "", false
	}
	return *result, true
}

// ClearConfigValueCache clears cached command results (tests).
func ClearConfigValueCache() {
	commandCacheMu.Lock()
	commandResultCache = map[string]*string{}
	commandCacheMu.Unlock()
}
