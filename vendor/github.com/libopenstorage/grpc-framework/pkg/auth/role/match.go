/*
Package role manages roles in Kvdb and provides validation
Copyright 2022 Pure Storage

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package role

import "strings"

const (
	invalidChars = "/ "
	negMatchChar = "!"
)

// Determines if the rules deny string s
func DenyRule(rule, s string) bool {
	if strings.HasPrefix(rule, negMatchChar) {
		return MatchRule(strings.TrimSpace(strings.Join(strings.Split(rule, negMatchChar), "")), s)
	}

	return false
}

// Determines if the rules apply to string s
// rule can be:
// '*' - match all
// '*xxx' - ends with xxx
// 'xxx*' - starts with xxx
// '*xxx*' - contains xxx
func MatchRule(rule, s string) bool {
	rule = strings.ToLower(rule)
	s = strings.ToLower(s)
	// no rule
	rl := len(rule)
	if rl == 0 {
		return false
	}

	// '*xxx' || 'xxx*'
	if rule[0:1] == "*" || rule[rl-1:rl] == "*" {
		// get the matching string from the rule
		match := strings.TrimSpace(strings.Join(strings.Split(rule, "*"), ""))

		// '*' or '*******'
		if len(match) == 0 {
			return true
		}

		// '*xxx*'
		if rule[0:1] == "*" && rule[rl-1:rl] == "*" {
			return strings.Contains(s, match)
		}

		// '*xxx'
		if rule[0:1] == "*" {
			return strings.HasSuffix(s, match)
		}

		// 'xxx*'
		return strings.HasPrefix(s, match)
	}

	// no wildcard stars given in rule
	return rule == s
}
