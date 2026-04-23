// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gate

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/bmbouter/alcove/internal"
)

// CheckPolicyRules evaluates an HTTP request against resolved policy rules.
// Default-deny: if no rule matches, the request is denied.
func CheckPolicyRules(method, requestURL string, rules []internal.PolicyRule) AccessResult {
	u, err := url.Parse(requestURL)
	if err != nil {
		return AccessResult{Allowed: false, Reason: "invalid URL"}
	}

	host := u.Hostname()
	path := u.Path

	for _, rule := range rules {
		if matchesRule(method, host, path, rule.Allow) {
			return AccessResult{
				Allowed:   true,
				Service:   host,
				Operation: method + " " + path,
			}
		}
	}

	return AccessResult{
		Allowed: false,
		Reason:  fmt.Sprintf("no policy rule allows %s %s", method, requestURL),
	}
}

// matchesRule checks if a request matches a single HTTP rule.
func matchesRule(method, host, path string, rule internal.HTTPRule) bool {
	// Method match: "*" matches any, otherwise case-insensitive exact
	if rule.Method != "*" && !strings.EqualFold(method, rule.Method) {
		return false
	}

	// Host match: exact or wildcard prefix (*.example.com)
	if !matchHost(host, rule.Host) {
		return false
	}

	// Path match: glob
	if !matchPathGlob(path, rule.Path) {
		return false
	}

	return true
}

// matchHost matches a hostname against a pattern. Supports:
// - Exact match: "api.github.com"
// - Wildcard prefix: "*.atlassian.net" (matches any subdomain)
// - Full wildcard: "*" (matches any host)
func matchHost(host, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.ToLower(pattern[1:]) // ".atlassian.net"
		return strings.HasSuffix(strings.ToLower(host), suffix)
	}
	return strings.EqualFold(host, pattern)
}

// matchPathGlob matches a URL path against a glob pattern.
// * matches exactly one path segment, ** matches zero or more segments.
func matchPathGlob(path, pattern string) bool {
	pathSegs := splitPath(path)
	patSegs := splitPath(pattern)
	return matchSegments(pathSegs, patSegs)
}

// splitPath splits a path by "/" and removes empty segments from the middle,
// but preserves the overall structure.
func splitPath(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// matchSegments recursively matches path segments against pattern segments.
func matchSegments(pathSegs, patSegs []string) bool {
	pi := 0 // path index
	pp := 0 // pattern index

	for pp < len(patSegs) {
		if patSegs[pp] == "**" {
			// ** matches zero or more segments (greedy, try all positions)
			// Try matching the rest of the pattern starting from each position
			remaining := patSegs[pp+1:]
			// Try matching zero segments through all remaining path segments
			for i := pi; i <= len(pathSegs); i++ {
				if matchSegments(pathSegs[i:], remaining) {
					return true
				}
			}
			return false
		}

		// Need a path segment to match against
		if pi >= len(pathSegs) {
			return false
		}

		if patSegs[pp] == "*" {
			// * matches exactly one segment
			pi++
			pp++
			continue
		}

		// Exact match (case-sensitive for paths)
		if pathSegs[pi] != patSegs[pp] {
			return false
		}

		pi++
		pp++
	}

	// Both must be exhausted
	return pi == len(pathSegs)
}
