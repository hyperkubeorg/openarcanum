package site

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
)

// Markdown renders announcement text: a small Markdown subset turned into
// HTML that is safe by construction. Every piece of the source is escaped
// as it is written out, and the only tags produced are the ones below, so
// raw HTML in the source is shown as text, never passed through:
//
//   - paragraphs (a blank line between them; a line ending in two spaces or
//     a backslash breaks the line), # / ## / ### headings, > quotes,
//   - / * / + and 1. lists (one level), ``` code blocks, --- rules;
//   - inline `code`, **bold**, *italic* / _italic_, backslash escapes;
//   - links [text](url) and <url>, only to https:, http:, mailto: or a path
//     on this site, opened without a Referer. Any other link (javascript:,
//     data:, protocol-relative //host …) is shown as its text alone.
func Markdown(src string) template.HTML {
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	src = strings.ReplaceAll(src, "\x00", "�")
	var b strings.Builder
	renderBlocks(&b, strings.Split(src, "\n"), 0)
	return template.HTML(b.String()) // #nosec G203 -- every byte of the source was escaped above
}

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t#]*$`)
	mdRule    = regexp.MustCompile(`^ {0,3}(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	mdBullet  = regexp.MustCompile(`^ {0,3}[-*+][ \t]+`)
	mdOrdered = regexp.MustCompile(`^ {0,3}\d{1,9}[.)][ \t]+`)
	mdFence   = regexp.MustCompile("^ {0,3}```")
	mdQuote   = regexp.MustCompile(`^ {0,3}> ?`)
)

const mdMaxDepth = 4

func renderBlocks(b *strings.Builder, lines []string, depth int) {
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.TrimSpace(line) == "":
			i++
		case mdFence.MatchString(line):
			j := i + 1
			for j < len(lines) && !mdFence.MatchString(lines[j]) {
				j++
			}
			b.WriteString("<pre><code>")
			b.WriteString(html.EscapeString(strings.Join(lines[i+1:j], "\n")))
			b.WriteString("</code></pre>\n")
			i = j + 1
		case mdHeading.MatchString(line):
			m := mdHeading.FindStringSubmatch(line)
			// The page title is h1 and each announcement's title h2, so the
			// body's headings start at h3.
			tag := []string{"h3", "h4", "h5", "h5", "h5", "h5"}[len(m[1])-1]
			b.WriteString("<" + tag + ">")
			renderInline(b, m[2], 0)
			b.WriteString("</" + tag + ">\n")
			i++
		case mdRule.MatchString(line):
			b.WriteString("<hr>\n")
			i++
		case mdQuote.MatchString(line):
			var inner []string
			for i < len(lines) && mdQuote.MatchString(lines[i]) {
				inner = append(inner, mdQuote.ReplaceAllString(lines[i], ""))
				i++
			}
			b.WriteString("<blockquote>\n")
			if depth < mdMaxDepth {
				renderBlocks(b, inner, depth+1)
			} else {
				renderParagraph(b, inner)
			}
			b.WriteString("</blockquote>\n")
		case mdBullet.MatchString(line) || mdOrdered.MatchString(line):
			marker, tag := mdBullet, "ul"
			if !mdBullet.MatchString(line) {
				marker, tag = mdOrdered, "ol"
			}
			b.WriteString("<" + tag + ">\n")
			for i < len(lines) && marker.MatchString(lines[i]) {
				item := []string{marker.ReplaceAllString(lines[i], "")}
				i++
				// Indented lines continue the item.
				for i < len(lines) && strings.TrimSpace(lines[i]) != "" && (strings.HasPrefix(lines[i], "  ") || strings.HasPrefix(lines[i], "\t")) &&
					!marker.MatchString(lines[i]) {
					item = append(item, strings.TrimSpace(lines[i]))
					i++
				}
				b.WriteString("<li>")
				renderInline(b, strings.Join(item, " "), 0)
				b.WriteString("</li>\n")
			}
			b.WriteString("</" + tag + ">\n")
		default:
			j := i
			for j < len(lines) && strings.TrimSpace(lines[j]) != "" && (j == i || !startsBlock(lines[j])) {
				j++
			}
			renderParagraph(b, lines[i:j])
			i = j
		}
	}
}

func startsBlock(line string) bool {
	return mdFence.MatchString(line) || mdHeading.MatchString(line) || mdRule.MatchString(line) ||
		mdQuote.MatchString(line) || mdBullet.MatchString(line) || mdOrdered.MatchString(line)
}

func renderParagraph(b *strings.Builder, lines []string) {
	b.WriteString("<p>")
	for k, l := range lines {
		brk := strings.HasSuffix(l, "  ") || (strings.HasSuffix(l, "\\") && !strings.HasSuffix(l, "\\\\"))
		l = strings.TrimSpace(l)
		if brk {
			l = strings.TrimSuffix(l, "\\")
		}
		renderInline(b, l, 0)
		if k < len(lines)-1 {
			if brk {
				b.WriteString("<br>\n")
			} else {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("</p>\n")
}

const mdEscapable = "\\`*_{}[]()#+-.!<>|~"

// renderInline writes s with inline markup; depth bounds nesting.
func renderInline(b *strings.Builder, s string, depth int) {
	var text strings.Builder
	flush := func() {
		b.WriteString(html.EscapeString(text.String()))
		text.Reset()
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte(mdEscapable, s[i+1]) >= 0:
			text.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				flush()
				b.WriteString("<code>" + html.EscapeString(s[i+1:i+1+j]) + "</code>")
				i += j + 2
				continue
			}
		case (c == '*' || c == '_') && depth < mdMaxDepth:
			delim := string(c)
			tag := "em"
			if strings.HasPrefix(s[i:], delim+delim) {
				delim, tag = delim+delim, "strong"
			}
			start := i + len(delim)
			if start < len(s) && s[start] != ' ' {
				if j := strings.Index(s[start:], delim); j > 0 && s[start+j-1] != ' ' {
					flush()
					b.WriteString("<" + tag + ">")
					renderInline(b, s[start:start+j], depth+1)
					b.WriteString("</" + tag + ">")
					i = start + j + len(delim)
					continue
				}
			}
		case c == '[' && depth < mdMaxDepth:
			if label, href, n, ok := mdLink(s[i:]); ok {
				flush()
				if u, ok := safeHref(href); ok {
					b.WriteString(`<a href="` + html.EscapeString(u) + `" rel="nofollow noopener noreferrer">`)
					renderInline(b, label, depth+1)
					b.WriteString("</a>")
				} else {
					renderInline(b, label, depth+1)
				}
				i += n
				continue
			}
		case c == '<':
			if j := strings.IndexByte(s[i:], '>'); j > 0 {
				if u, ok := safeHref(s[i+1 : i+j]); ok && strings.Contains(u, ":") {
					flush()
					b.WriteString(`<a href="` + html.EscapeString(u) + `" rel="nofollow noopener noreferrer">` + html.EscapeString(u) + "</a>")
					i += j + 1
					continue
				}
			}
		}
		text.WriteByte(c)
		i++
	}
	flush()
}

// mdLink parses "[label](href)" at the start of s, returning its length.
func mdLink(s string) (label, href string, n int, ok bool) {
	level := 0
	end := -1
	for k := 0; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '[':
			level++
		case ']':
			level--
			if level == 0 {
				end = k
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || end+1 >= len(s) || s[end+1] != '(' {
		return "", "", 0, false
	}
	cl := strings.IndexByte(s[end+2:], ')')
	if cl < 0 {
		return "", "", 0, false
	}
	return s[1:end], strings.TrimSpace(s[end+2 : end+2+cl]), end + 3 + cl, true
}

// safeHref accepts http(s) and mailto URLs and paths on this site.
func safeHref(raw string) (string, bool) {
	if raw == "" || len(raw) > 2048 {
		return "", false
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f || r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' {
			return "", false
		}
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw, true
	}
	if strings.HasPrefix(raw, "#") {
		return raw, true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http":
		if u.Host == "" || u.User != nil {
			return "", false
		}
	case "mailto":
		if u.Opaque == "" {
			return "", false
		}
	default:
		return "", false
	}
	return u.String(), true
}
