package execx

import (
	"os"
	"strings"
)

// ToolEnv names the environment variables a child needs in order to find and
// run the tools reviewd drives: gh, git and claude.
//
// PATH alone is not enough when those tools are version-manager shims. aqua's
// shim resolves the real binary through AQUA_GLOBAL_CONFIG, so a bare `gh` on
// PATH still fails -- with `gh: command is not found` from aqua itself, not
// from the shell. Pinning gh_path to the versioned package directory would
// work too, but it breaks on the next `aqua up`.
//
// Finding the binary is only half of it: gh also has to find its own config.
// It looks for hosts.yml under XDG_CONFIG_HOME (or GH_CONFIG_DIR), so a shell
// that moves XDG_CONFIG_HOME out of ~/.config leaves the child looking at an
// empty directory and failing with `To get started with GitHub CLI, please
// run: gh auth login` -- authenticated in the terminal, unauthenticated
// wherever reviewd runs it.
//
// Two places need this, and both start from a bare environment:
//   - the launchd plist, because launchd gives an agent PATH=/usr/bin:/bin:...
//   - the Ghostty tab, because Ghostty is a GUI app launched by launchd and
//     inherits the same minimal PATH. A tab that runs `claude` directly dies
//     with `claude: not found` before the session ever starts.
var ToolEnv = []string{
	"AQUA_GLOBAL_CONFIG",
	"AQUA_ROOT_DIR",
	"AQUA_POLICY_CONFIG",
	"AQUA_DISABLE_POLICY",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"GH_CONFIG_DIR",
	"CLAUDE_CONFIG_DIR",
}

// CollectToolEnv reads ToolEnv from the current process, skipping anything
// unset or empty.
func CollectToolEnv() map[string]string {
	out := make(map[string]string, len(ToolEnv))
	for _, name := range ToolEnv {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			out[name] = value
		}
	}
	return out
}

// ToolEnviron renders the current process's PATH, HOME and ToolEnv as
// KEY=VALUE entries, ready to hand to a child that would otherwise start with
// an environment too bare to resolve gh or claude.
func ToolEnviron() []string {
	out := make([]string, 0, len(ToolEnv)+2)
	for _, name := range []string{"PATH", "HOME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			out = append(out, name+"="+value)
		}
	}
	// Walked in ToolEnv order rather than over the map, so the list a caller
	// hands to a child is stable from one run to the next.
	collected := CollectToolEnv()
	for _, name := range ToolEnv {
		if value, ok := collected[name]; ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}
