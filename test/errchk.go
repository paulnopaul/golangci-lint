package test

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var errorLineRx = regexp.MustCompile(`^\S+?: (.*)\((\S+?)\)$`)

// errorCheck matches errors in outStr against comments in source files.
// For each line of the source files which should generate an error,
// there should be a comment of the form // ERROR "regexp".
// If outStr has an error for a line which has no such comment,
// this function will report an error.
// Likewise if outStr does not have an error for a line which has a comment,
// or if the error message does not match the <regexp>.
// The <regexp> syntax is Perl but it's best to stick to egrep.
//
// Sources files are supplied as fullshort slice.
// It consists of pairs: full path to source file and its base name.
//
//nolint:gocyclo,funlen
func errorCheck(outStr string, wantAuto bool, defaultWantedLinter string, fullshort ...string) (err error) {
	var errs []error
	out := splitOutput(outStr, wantAuto)
	// Cut directory name.
	for i := range out {
		for j := 0; j < len(fullshort); j += 2 {
			full, short := fullshort[j], fullshort[j+1]
			out[i] = strings.Replace(out[i], full, short, -1)
		}
	}

	var want []wantedError
	for j := 0; j < len(fullshort); j += 2 {
		full, short := fullshort[j], fullshort[j+1]
		want = append(want, wantedErrors(full, short, defaultWantedLinter)...)
	}
	for _, we := range want {
		if we.linter == "" {
			err := fmt.Errorf("%s:%d: no expected linter indicated for test",
				we.file, we.lineNum)
			errs = append(errs, err)
			continue
		}

		var errmsgs []string
		if we.auto {
			errmsgs, out = partitionStrings("<autogenerated>", out)
		} else {
			errmsgs, out = partitionStrings(we.prefix, out)
		}
		if len(errmsgs) == 0 {
			errs = append(errs, fmt.Errorf("%s:%d: missing error %q", we.file, we.lineNum, we.reStr))
			continue
		}
		matched := false
		var textsToMatch []string
		for _, errmsg := range errmsgs {
			// Assume errmsg says "file:line: foo (<linter>)".
			matches := errorLineRx.FindStringSubmatch(errmsg)
			if len(matches) == 0 {
				err := fmt.Errorf("%s:%d: unexpected error line: %s",
					we.file, we.lineNum, errmsg)
				errs = append(errs, err)
				continue
			}

			text, actualLinter := matches[1], matches[2]

			if we.re.MatchString(text) {
				matched = true
			} else {
				out = append(out, errmsg)
				textsToMatch = append(textsToMatch, text)
			}

			if actualLinter != we.linter {
				err := fmt.Errorf("%s:%d: expected error from %q but got error from %q in:\n\t%s",
					we.file, we.lineNum, we.linter, actualLinter, strings.Join(out, "\n\t"))
				errs = append(errs, err)
			}
		}
		if !matched {
			err := fmt.Errorf("%s:%d: no match for %#q vs %q in:\n\t%s",
				we.file, we.lineNum, we.reStr, textsToMatch, strings.Join(out, "\n\t"))
			errs = append(errs, err)
			continue
		}
	}

	if len(out) > 0 {
		errs = append(errs, fmt.Errorf("unmatched errors"))
		for _, errLine := range out {
			errs = append(errs, fmt.Errorf("%s", errLine))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\n")
	for _, err := range errs {
		fmt.Fprintf(&buf, "%s\n", err.Error())
	}
	return errors.New(buf.String())
}

func splitOutput(out string, wantAuto bool) []string {
	// gc error messages continue onto additional lines with leading tabs.
	// Split the output at the beginning of each line that doesn't begin with a tab.
	// <autogenerated> lines are impossible to match so those are filtered out.
	var res []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r") // normalize Windows output
		if strings.HasPrefix(line, "\t") {
			res[len(res)-1] += "\n" + line
		} else if strings.HasPrefix(line, "go tool") || strings.HasPrefix(line, "#") || !wantAuto && strings.HasPrefix(line, "<autogenerated>") {
			continue
		} else if strings.TrimSpace(line) != "" {
			res = append(res, line)
		}
	}
	return res
}

// matchPrefix reports whether s starts with file name prefix followed by a :,
// and possibly preceded by a directory name.
func matchPrefix(s, prefix string) bool {
	i := strings.Index(s, ":")
	if i < 0 {
		return false
	}
	j := strings.LastIndex(s[:i], "/")
	s = s[j+1:]
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return false
	}
	if s[len(prefix)] == ':' {
		return true
	}
	return false
}

func partitionStrings(prefix string, strs []string) (matched, unmatched []string) {
	for _, s := range strs {
		if matchPrefix(s, prefix) {
			matched = append(matched, s)
		} else {
			unmatched = append(unmatched, s)
		}
	}
	return
}

type wantedError struct {
	reStr   string
	re      *regexp.Regexp
	lineNum int
	auto    bool // match <autogenerated> line
	file    string
	prefix  string
	linter  string
}

var (
	errRx          = regexp.MustCompile(`// (?:GC_)?ERROR (.*)`)
	errAutoRx      = regexp.MustCompile(`// (?:GC_)?ERRORAUTO (.*)`)
	linterPrefixRx = regexp.MustCompile("^\\s*([^\\s\"`]+)")
)

// wantedErrors parses expected errors from comments in a file.
//
//nolint:nakedret
func wantedErrors(file, short, defaultLinter string) (errs []wantedError) {
	cache := make(map[string]*regexp.Regexp)

	src, err := os.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		lineNum := i + 1
		if strings.Contains(line, "////") {
			// double comment disables ERROR
			continue
		}
		var auto bool
		m := errAutoRx.FindStringSubmatch(line)
		if m != nil {
			auto = true
		} else {
			m = errRx.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		rest := m[1]
		linter := defaultLinter
		if lm := linterPrefixRx.FindStringSubmatch(rest); lm != nil {
			linter = lm[1]
			rest = rest[len(lm[0]):]
		}
		rx, err := strconv.Unquote(strings.TrimSpace(rest))
		if err != nil {
			log.Fatalf("%s:%d: invalid errchk line: %s, %v", file, lineNum, line, err)
		}
		re := cache[rx]
		if re == nil {
			var err error
			re, err = regexp.Compile(rx)
			if err != nil {
				log.Fatalf("%s:%d: invalid regexp \"%#q\" in ERROR line: %v", file, lineNum, rx, err)
			}
			cache[rx] = re
		}
		prefix := fmt.Sprintf("%s:%d", short, lineNum)
		errs = append(errs, wantedError{
			reStr:   rx,
			re:      re,
			prefix:  prefix,
			auto:    auto,
			lineNum: lineNum,
			file:    short,
			linter:  linter,
		})
	}

	return
}
