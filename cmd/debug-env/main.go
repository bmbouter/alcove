package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

var Version = "dev"

type EnvEntry struct {
	Key            string `json:"key"`
	Value          string `json:"value,omitempty"`
	MaskedValue    string `json:"masked_value"`
	Classification string `json:"classification"` // dummy, sensitive, safe, gate-proxy
	Category       string `json:"category"`
}

var categoryOrder = []string{
	"Infrastructure",
	"LLM Provider",
	"SCM Tokens",
	"SCM Gateway URLs",
	"Network Proxy",
	"Plugins & Catalog",
	"Git Config",
	"Generic Secrets",
	"Runtime",
	"Other",
}

var infraVars = map[string]bool{
	"TASK_ID": true, "SESSION_ID": true, "SESSION_TOKEN": true,
	"HAIL_URL": true, "LEDGER_URL": true, "TASK_TIMEOUT": true,
	"TASK_BUDGET": true, "PROMPT": true,
}

var llmVars = map[string]bool{
	"ANTHROPIC_BASE_URL": true, "ANTHROPIC_API_KEY": true,
	"ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_MODEL": true,
	"PROVIDER": true,
}

var scmTokenVars = map[string]bool{
	"GITHUB_TOKEN": true, "GH_TOKEN": true, "GITHUB_PERSONAL_ACCESS_TOKEN": true,
	"GITLAB_TOKEN": true, "GITLAB_PERSONAL_ACCESS_TOKEN": true,
	"JIRA_TOKEN": true, "SPLUNK_TOKEN": true,
}

var scmURLVars = map[string]bool{
	"GITHUB_API_URL": true, "GH_HOST": true, "GH_PROTOCOL": true,
	"GH_PROMPT_DISABLED": true, "GH_NO_UPDATE_NOTIFIER": true,
	"GITLAB_API_URL": true, "GLAB_HOST": true,
	"JIRA_API_URL": true, "SPLUNK_URL": true,
}

var proxyVars = map[string]bool{
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
}

var pluginVars = map[string]bool{
	"ALCOVE_PLUGINS": true, "ALCOVE_SKILL_REPOS": true,
	"ALCOVE_MCP_CONFIG": true, "ALCOVE_EXECUTABLE": true,
}

var gitVars = map[string]bool{
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"GIT_TERMINAL_PROMPT": true, "GIT_SSH_COMMAND": true,
	"GATE_CREDENTIAL_URL": true, "REPO": true, "BRANCH": true,
}

var runtimeVars = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "SHELL": true,
	"GOPATH": true, "GOBIN": true, "GOROOT": true, "LANG": true,
	"TERM": true, "HOSTNAME": true, "PWD": true, "OLDPWD": true,
	"SHLVL": true, "LC_ALL": true, "TMPDIR": true,
}

func categorize(key string) string {
	switch {
	case infraVars[key]:
		return "Infrastructure"
	case llmVars[key]:
		return "LLM Provider"
	case scmTokenVars[key]:
		return "SCM Tokens"
	case scmURLVars[key]:
		return "SCM Gateway URLs"
	case proxyVars[key]:
		return "Network Proxy"
	case pluginVars[key]:
		return "Plugins & Catalog"
	case gitVars[key]:
		return "Git Config"
	case runtimeVars[key]:
		return "Runtime"
	default:
		// Check if it looks like a secret/credential
		upper := strings.ToUpper(key)
		for _, word := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "CREDENTIAL", "AUTH"} {
			if strings.Contains(upper, word) {
				return "Generic Secrets"
			}
		}
		return "Other"
	}
}

func isDummy(value string) bool {
	return strings.HasPrefix(value, "alcove-session-") ||
		value == "sk-placeholder-routed-through-gate"
}

func isGateProxy(value string) bool {
	return strings.Contains(value, "gate-") && strings.Contains(value, ":8443")
}

func classify(key, value string) string {
	if isDummy(value) {
		return "dummy"
	}
	if isGateProxy(value) {
		return "gate-proxy"
	}
	cat := categorize(key)
	if cat == "Generic Secrets" {
		return "sensitive"
	}
	if key == "SESSION_TOKEN" || key == "ANTHROPIC_AUTH_TOKEN" {
		return "sensitive"
	}
	return "safe"
}

func mask(value string) string {
	if len(value) <= 8 {
		return "***"
	}
	return value[:8] + "..."
}

func main() {
	showSecrets := flag.Bool("show-secrets", false, "Show full values of sensitive environment variables")
	jsonOutput := flag.Bool("json", false, "Output as JSON")
	version := flag.Bool("version", false, "Print version")
	flag.Parse()

	if *version {
		fmt.Printf("debug-env %s\n", Version)
		os.Exit(0)
	}

	// Collect and categorize
	categories := make(map[string][]EnvEntry)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]
		cat := categorize(key)
		cls := classify(key, value)

		entry := EnvEntry{
			Key:            key,
			Classification: cls,
			Category:       cat,
		}

		if cls == "sensitive" && !*showSecrets {
			entry.MaskedValue = mask(value)
		} else {
			entry.Value = value
			entry.MaskedValue = value
		}

		categories[cat] = append(categories[cat], entry)
	}

	// Sort entries within each category
	for cat := range categories {
		sort.Slice(categories[cat], func(i, j int) bool {
			return categories[cat][i].Key < categories[cat][j].Key
		})
	}

	if *jsonOutput {
		type CategoryGroup struct {
			Name    string     `json:"name"`
			Entries []EnvEntry `json:"entries"`
		}
		var groups []CategoryGroup
		for _, cat := range categoryOrder {
			if entries, ok := categories[cat]; ok && len(entries) > 0 {
				groups = append(groups, CategoryGroup{Name: cat, Entries: entries})
			}
		}
		out := map[string]any{
			"version":    Version,
			"categories": groups,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}

	// Human-readable output
	for _, cat := range categoryOrder {
		entries, ok := categories[cat]
		if !ok || len(entries) == 0 {
			continue
		}
		fmt.Printf("\n=== %s ===\n", cat)
		for _, e := range entries {
			display := e.MaskedValue
			if e.Value != "" {
				display = e.Value
			}

			// Truncate very long values (like JSON configs)
			if len(display) > 120 {
				display = display[:120] + "..."
			}

			annotation := ""
			switch e.Classification {
			case "dummy":
				annotation = " [DUMMY]"
			case "sensitive":
				annotation = " [MASKED]"
			case "gate-proxy":
				annotation = " [GATE-PROXY]"
			}

			fmt.Printf("  %-30s = %s%s\n", e.Key, display, annotation)
		}
	}
	fmt.Println()
}
