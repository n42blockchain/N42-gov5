package prometheus

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// identRegexp validates Prometheus metric and label identifiers.
var identRegexp = regexp.MustCompile(`^[a-zA-Z_:.][a-zA-Z0-9_:.]*$`)

// parseMetric parses a Prometheus metric string into its name and optional labels.
// For example, "foo{bar=\"baz\"}" returns ("foo", {"bar": "baz"}, nil).
func parseMetric(s string) (string, prometheus.Labels, error) {
	if len(s) == 0 {
		return "", nil, fmt.Errorf("metric cannot be empty")
	}

	n := strings.IndexByte(s, '{')
	if n < 0 {
		if err := validateIdent(s); err != nil {
			return "", nil, err
		}
		return s, nil, nil
	}

	ident := s[:n]
	s = s[n+1:]
	if err := validateIdent(ident); err != nil {
		return "", nil, err
	}
	if len(s) == 0 || s[len(s)-1] != '}' {
		return "", nil, fmt.Errorf("missing closing curly brace at the end of %q", ident)
	}

	tags, err := parseTags(s[:len(s)-1])
	if err != nil {
		return "", nil, err
	}
	return ident, tags, nil
}

// parseTags parses a comma-separated key="value" label string.
func parseTags(s string) (prometheus.Labels, error) {
	if len(s) == 0 {
		return nil, nil
	}

	var labels prometheus.Labels

	for {
		n := strings.IndexByte(s, '=')
		if n < 0 {
			return nil, fmt.Errorf("missing `=` after %q", s)
		}
		ident := s[:n]
		s = s[n+1:]
		if err := validateIdent(ident); err != nil {
			return nil, err
		}
		if len(s) == 0 || s[0] != '"' {
			return nil, fmt.Errorf("missing starting `\"` for %q value; tail=%q", ident, s)
		}
		s = s[1:]

		var value string
		for {
			n = strings.IndexByte(s, '"')
			if n < 0 {
				return nil, fmt.Errorf("missing trailing `\"` for %q value; tail=%q", ident, s)
			}
			// Count consecutive backslashes before the quote
			m := n
			for m > 0 && s[m-1] == '\\' {
				m--
			}
			// Odd number of backslashes means the quote is escaped
			if (n-m)%2 == 1 {
				value += s[:n]
				s = s[n+1:]
				continue
			}
			value += s[:n]
			if labels == nil {
				labels = prometheus.Labels{}
			}
			labels[ident] = value
			s = s[n+1:]
			if len(s) == 0 {
				return labels, nil
			}
			if s[0] != ',' {
				return nil, fmt.Errorf("missing `,` after %q value; tail=%q", ident, s)
			}
			s = strings.TrimLeft(s[1:], " ")
			break
		}
	}
}

func validateIdent(s string) error {
	if !identRegexp.MatchString(s) {
		return fmt.Errorf("invalid identifier %q", s)
	}
	return nil
}
