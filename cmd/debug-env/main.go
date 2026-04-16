package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

// PluginSpec describes a plugin requested via ALCOVE_PLUGINS.
type PluginSpec struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Ref    string `json:"ref,omitempty"`
}

// SkillRepo describes a skill repo requested via ALCOVE_SKILL_REPOS.
type SkillRepo struct {
	URL     string `json:"url"`
	Ref     string `json:"ref,omitempty"`
	Name    string `json:"name,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// InstallationReport summarizes what was requested and what is actually installed.
type InstallationReport struct {
	Plugins     []InstallEntry `json:"plugins,omitempty"`
	SkillRepos  []InstallEntry `json:"skill_repos,omitempty"`
	MCPServers  []InstallEntry `json:"mcp_servers,omitempty"`
	Credentials []InstallEntry `json:"credentials,omitempty"`
}

// InstallEntry is a single item in the installation report.
type InstallEntry struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source,omitempty"`
	Status string `json:"status"` // installed, missing, configured, requested, dummy, sensitive
	Detail string `json:"detail,omitempty"`
}

func checkInstallation() *InstallationReport {
	// Skip if not inside a Skiff container
	if os.Getenv("TASK_ID") == "" {
		return nil
	}

	report := &InstallationReport{}

	// Parse and check plugins
	if raw := os.Getenv("ALCOVE_PLUGINS"); raw != "" {
		var plugins []PluginSpec
		json.Unmarshal([]byte(raw), &plugins)
		for _, p := range plugins {
			entry := InstallEntry{Name: p.Name, Source: p.Source, Type: "plugin"}
			// Check /tmp/alcove-plugins/{name}
			dir := "/tmp/alcove-plugins/" + p.Name
			if _, err := os.Stat(dir); err == nil {
				entry.Status = "installed"
			} else if p.Source == "claude-plugins-official" || p.Source == "" {
				// Marketplace/official plugins installed differently
				entry.Status = "requested" // Can't easily verify marketplace installs
			} else {
				entry.Status = "missing"
			}
			report.Plugins = append(report.Plugins, entry)
		}
	}

	// Parse and check skill repos
	if raw := os.Getenv("ALCOVE_SKILL_REPOS"); raw != "" {
		var repos []SkillRepo
		json.Unmarshal([]byte(raw), &repos)
		for _, r := range repos {
			name := r.Name
			if name == "" {
				name = filepath.Base(strings.TrimSuffix(r.URL, ".git"))
			}
			entry := InstallEntry{Name: name, Source: r.URL, Type: "skill_repo"}
			dir := "/tmp/alcove-skills/" + name
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				// Check if it's a lola module or plugin dir
				for _, sub := range []string{"module", "skills", "agents"} {
					if _, err := os.Stat(filepath.Join(dir, sub)); err == nil {
						entry.Detail = "lola module"
						break
					}
				}
				if entry.Detail == "" {
					entry.Detail = "plugin dir"
				}
				entry.Status = "installed"
			} else {
				entry.Status = "missing"
			}
			report.SkillRepos = append(report.SkillRepos, entry)
		}
	}

	// Parse MCP config
	if raw := os.Getenv("ALCOVE_MCP_CONFIG"); raw != "" {
		var mcpConfig map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &mcpConfig)
		for name := range mcpConfig {
			entry := InstallEntry{Name: name, Type: "mcp_server", Status: "configured"}
			report.MCPServers = append(report.MCPServers, entry)
		}
	}

	// Also check ~/.claude.json for MCP servers
	if data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude.json")); err == nil {
		var claudeConfig map[string]json.RawMessage
		json.Unmarshal(data, &claudeConfig)
		if servers, ok := claudeConfig["mcpServers"]; ok {
			var serverMap map[string]json.RawMessage
			json.Unmarshal(servers, &serverMap)
			for name := range serverMap {
				// Only add if not already in MCP list
				found := false
				for _, s := range report.MCPServers {
					if s.Name == name {
						found = true
						break
					}
				}
				if !found {
					report.MCPServers = append(report.MCPServers, InstallEntry{Name: name, Type: "mcp_server", Status: "configured", Detail: "from claude.json"})
				}
			}
		}
	}

	// Credential summary
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]
		cls := classify(key, value)
		if cls == "dummy" || cls == "sensitive" {
			detail := "real value"
			if isDummy(value) {
				detail = "gate-proxied"
			}
			report.Credentials = append(report.Credentials, InstallEntry{
				Name:   key,
				Status: cls,
				Type:   "credential",
				Detail: detail,
			})
		}
	}

	sort.Slice(report.Credentials, func(i, j int) bool { return report.Credentials[i].Name < report.Credentials[j].Name })

	return report
}

func printInstallationReport(r *InstallationReport) {
	fmt.Println("\n=== Installation Status ===")

	if len(r.Plugins) > 0 {
		fmt.Printf("\n  Plugins (requested: %d):\n", len(r.Plugins))
		for _, p := range r.Plugins {
			icon := "[OK]  "
			if p.Status == "missing" {
				icon = "[MISS]"
			}
			source := p.Source
			if source == "" {
				source = "marketplace"
			}
			fmt.Printf("    %s %-25s (%s)\n", icon, p.Name, source)
		}
	}

	if len(r.SkillRepos) > 0 {
		fmt.Printf("\n  Skill Repos (requested: %d):\n", len(r.SkillRepos))
		for _, s := range r.SkillRepos {
			icon := "[OK]  "
			if s.Status == "missing" {
				icon = "[MISS]"
			}
			detail := ""
			if s.Detail != "" {
				detail = " (" + s.Detail + ")"
			}
			fmt.Printf("    %s %-25s%s\n", icon, s.Name, detail)
		}
	}

	if len(r.MCPServers) > 0 {
		fmt.Printf("\n  MCP Servers (configured: %d):\n", len(r.MCPServers))
		for _, m := range r.MCPServers {
			detail := ""
			if m.Detail != "" {
				detail = " — " + m.Detail
			}
			fmt.Printf("    [OK]   %-25s%s\n", m.Name, detail)
		}
	}

	if len(r.Credentials) > 0 {
		fmt.Printf("\n  Credentials (%d):\n", len(r.Credentials))
		for _, c := range r.Credentials {
			badge := "[REAL] "
			if c.Status == "dummy" {
				badge = "[DUMMY]"
			}
			detail := ""
			if c.Detail != "" {
				detail = " " + c.Detail
			}
			fmt.Printf("    %-30s %s%s\n", c.Name, badge, detail)
		}
	}

	fmt.Println()
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

	// Installation status (only inside Skiff)
	report := checkInstallation()

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
		if report != nil {
			out["installation"] = report
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

	if report != nil {
		printInstallationReport(report)
	} else {
		fmt.Println()
	}
}
