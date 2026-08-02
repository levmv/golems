package filesearch

import (
	"errors"
	"fmt"
	"strings"
)

const maxBraceExpansions = 128
const directEndSegment = ".filesearch-match-end"

type directPattern struct {
	include bool
	dirOnly bool
	matcher *ruleMatcher
}

func compileDirectPattern(raw string) (*directPattern, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("filesearch: glob is required")
	}
	if strings.IndexByte(raw, 0) >= 0 || strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("filesearch: glob contains an invalid character")
	}

	include := true
	pattern := raw
	if strings.HasPrefix(pattern, "!") {
		include = false
		pattern = pattern[1:]
		if pattern == "" {
			return nil, errors.New("filesearch: negative glob has no pattern")
		}
	}
	dirOnly := strings.HasSuffix(pattern, "/")
	if dirOnly {
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			return nil, errors.New("filesearch: directory glob has no pattern")
		}
	}
	expanded, err := expandBraces(pattern)
	if err != nil {
		return nil, fmt.Errorf("filesearch: compile glob %q: %w", raw, err)
	}
	for index := range expanded {
		expanded[index] = anchorDirectPattern(expanded[index])
	}
	matcher := newRuleMatcher()
	if patternErrs := matcher.addPatterns([]byte(strings.Join(expanded, "\n")), ""); len(patternErrs) > 0 {
		return nil, fmt.Errorf("filesearch: compile glob %q: %s", raw, patternErrs[0].Message)
	}
	return &directPattern{include: include, dirOnly: dirOnly, matcher: matcher}, nil
}

// gitignore rules propagate a directory match to all descendants, while a
// direct query glob is tested against each path independently. Matching a
// synthetic final segment prevents that propagation. Basename globs need an
// explicit ** prefix because adding the final segment otherwise makes them
// root-anchored.
func anchorDirectPattern(pattern string) string {
	if !strings.Contains(pattern, "/") {
		pattern = "**/" + pattern
	}
	return pattern + "/" + directEndSegment
}

func (p *directPattern) matches(path string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return p.matcher.matches(path+"/"+directEndSegment, false)
}

func expandBraces(pattern string) ([]string, error) {
	results := []string{pattern}
	for {
		expandedAny := false
		next := make([]string, 0, len(results))
		for _, current := range results {
			open, close, alternatives, ok, err := firstBraceGroup(current)
			if err != nil {
				return nil, err
			}
			if !ok {
				next = append(next, current)
				continue
			}
			expandedAny = true
			for _, alternative := range alternatives {
				if alternative == "" {
					return nil, errors.New("empty brace alternatives are not supported")
				}
				next = append(next, current[:open]+alternative+current[close+1:])
				if len(next) > maxBraceExpansions {
					return nil, fmt.Errorf("brace expansion exceeds %d alternatives", maxBraceExpansions)
				}
			}
		}
		results = next
		if !expandedAny {
			return results, nil
		}
	}
}

func firstBraceGroup(pattern string) (open, close int, alternatives []string, ok bool, err error) {
	open = -1
	depth := 0
	inClass := false
	escaped := false
	for index := 0; index < len(pattern); index++ {
		char := pattern[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '[' && !inClass {
			inClass = true
			continue
		}
		if char == ']' && inClass {
			inClass = false
			continue
		}
		if inClass {
			continue
		}
		switch char {
		case '{':
			if depth == 0 {
				open = index
			}
			depth++
		case '}':
			if depth == 0 {
				return 0, 0, nil, false, errors.New("unmatched closing brace")
			}
			depth--
			if depth == 0 {
				close = index
				parts := splitBraceAlternatives(pattern[open+1 : close])
				if len(parts) < 2 {
					// A brace pair without a comma is a literal glob fragment.
					open = -1
					continue
				}
				return open, close, parts, true, nil
			}
		}
	}
	if depth != 0 {
		return 0, 0, nil, false, errors.New("unmatched opening brace")
	}
	return 0, 0, nil, false, nil
}

func splitBraceAlternatives(body string) []string {
	var parts []string
	start := 0
	depth := 0
	inClass := false
	escaped := false
	for index := 0; index < len(body); index++ {
		char := body[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '[' && !inClass {
			inClass = true
			continue
		}
		if char == ']' && inClass {
			inClass = false
			continue
		}
		if inClass {
			continue
		}
		switch char {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:index])
				start = index + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}
