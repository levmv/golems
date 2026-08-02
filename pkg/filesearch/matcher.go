package filesearch

import gitignore "github.com/git-pkgs/gitignore"

// ruleMatcher is the only seam between filesearch and the temporary external
// matcher dependency. No dependency type or behavior is exposed to callers or
// to traversal code.
type ruleMatcher struct {
	matcher *gitignore.Matcher
}

type matcherPatternError struct {
	Pattern string
	Message string
}

func newRuleMatcher() *ruleMatcher {
	return &ruleMatcher{matcher: gitignore.New("")}
}

func (m *ruleMatcher) addPatterns(data []byte, dir string) []matcherPatternError {
	before := len(m.matcher.Errors())
	m.matcher.AddPatterns(data, dir)
	externalErrors := m.matcher.Errors()
	if len(externalErrors) == before {
		return nil
	}
	errs := make([]matcherPatternError, 0, len(externalErrors)-before)
	for _, externalErr := range externalErrors[before:] {
		errs = append(errs, matcherPatternError{
			Pattern: externalErr.Pattern,
			Message: externalErr.Message,
		})
	}
	return errs
}

func (m *ruleMatcher) matches(path string, isDir bool) bool {
	return m.matcher.MatchPath(path, isDir)
}

func (m *ruleMatcher) decision(path string, isDir bool) (matched, ignored bool) {
	result := m.matcher.MatchDetail(pathWithDirectorySuffix(path, isDir))
	return result.Matched, result.Ignored
}
