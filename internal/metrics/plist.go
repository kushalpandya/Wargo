// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	plistMaxDepth = 32
	plistMaxBytes = 8 << 20
)

var errPlistTooLarge = errors.New("plist: document too large")

// decodePlist parses an Apple XML property list. It returns map[string]any,
// []any, string, int64, float64, bool or []byte. Dates become strings.
// Nesting deeper than 32 levels or a document over 8 MiB is an error.
func decodePlist(r io.Reader) (any, error) {
	d := xml.NewDecoder(&plistLimit{r: r, n: plistMaxBytes})
	d.Strict = true
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "plist" {
			return plistChild(d, 1)
		}
	}
}

// plistChild reads the next value element inside the current parent.
func plistChild(d *xml.Decoder, depth int) (any, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return plistValue(d, t, depth)
		case xml.EndElement:
			return nil, errors.New("plist: empty container")
		}
	}
}

func plistText(d *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			return sb.String(), nil
		case xml.StartElement:
			return "", errors.New("plist: unexpected element " + t.Name.Local)
		}
	}
}

func plistValue(d *xml.Decoder, se xml.StartElement, depth int) (any, error) {
	if depth > plistMaxDepth {
		return nil, errors.New("plist: nested too deeply")
	}
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return m, nil
			case xml.StartElement:
				if t.Name.Local != "key" {
					return nil, errors.New("plist: expected key")
				}
				k, err := plistText(d)
				if err != nil {
					return nil, err
				}
				v, err := plistChild(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
		}
	case "array":
		a := []any{}
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return a, nil
			case xml.StartElement:
				v, err := plistValue(d, t, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
		}
	case "string", "date":
		return plistText(d)
	case "integer":
		s, err := plistText(d)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	case "real":
		s, err := plistText(d)
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	case "data":
		s, err := plistText(d)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	case "true", "false":
		if _, err := plistText(d); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	}
	return nil, errors.New("plist: unknown element " + se.Name.Local)
}

// plistLimit reads at most n bytes and then fails, so an oversized
// document is an error instead of a silently truncated one.
// A read after exactly n bytes returns errPlistTooLarge, not io.EOF, even
// if the source is at its end; decodePlist stops at the closing root
// value, so a complete document of n bytes or less still decodes.
type plistLimit struct {
	r io.Reader
	n int64
}

func (l *plistLimit) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errPlistTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}
