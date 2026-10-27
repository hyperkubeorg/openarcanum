package site

import (
	"encoding/base64"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Field limits (characters).
const (
	maxTitle       = 200
	minTitle       = 5
	maxDescription = 20_000
	minDescription = 20
	maxSteps       = 20_000
	maxVersion     = 200
	maxName        = 64
	maxEmail       = 254
	// maxFormBytes bounds a form post's body.
	maxFormBytes = 128 << 10
)

// ContactCodePrefix starts a contact code: a signed contact card in
// unpadded base64url that the app gives out (Direct messages → Contacts →
// Your contact code) so someone can message its owner without sharing a
// community. The maintainers add it in the app to reply (client
// dm.parse_contact_code checks the signature there).
const ContactCodePrefix = "openarcanum-contact:"

// MaxContactCode bounds a contact code. One device's card is about 900
// characters; a card can list up to 20 devices.
const MaxContactCode = 16 << 10

// PermanentCodePrefix starts a permanent contact code (the app's "Permanent
// code"): it names where the owner's current card is kept, so it keeps
// working after a week.
const PermanentCodePrefix = "openarcanum-id:"

// ValidContactCode reports whether s looks like a contact code, one-week or
// permanent: the prefix and a body that decodes.
func ValidContactCode(s string) bool {
	if len(s) > MaxContactCode {
		return false
	}
	body, ok := strings.CutPrefix(s, ContactCodePrefix)
	if !ok {
		body, ok = strings.CutPrefix(s, PermanentCodePrefix)
	}
	if !ok || len(body) < 64 {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(body)
	return err == nil
}

// cleanText normalises a submitted text field: valid UTF-8, \n line ends,
// no control characters other than newlines and tabs (and no newlines at
// all when singleLine), trimmed.
func cleanText(s string, singleLine bool) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	for _, r := range s {
		if (r == '\n' && !singleLine) || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩') {
			return "", false
		}
	}
	return strings.TrimSpace(s), true
}

// reportForm is a report form's fields, as sent and as shown again with
// errors.
type reportForm struct {
	Kind        string
	Title       string
	Description string
	Steps       string
	Version     string
	ContactCode string
	Name        string
	Email       string
	Severity    string
	Errors      map[string]string
}

func (f *reportForm) fail(field, msg string) {
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	if _, ok := f.Errors[field]; !ok {
		f.Errors[field] = msg
	}
}

// parseReport reads and validates a report form; f.Errors is empty when it
// is acceptable.
func parseReport(kind string, v url.Values) *reportForm {
	f := &reportForm{Kind: kind}
	text := func(field string, singleLine bool, minLen, maxLen int, required bool, label string) string {
		raw := v.Get(field)
		s, ok := cleanText(raw, singleLine)
		n := utf8.RuneCountInString(s)
		switch {
		case !ok:
			f.fail(field, label+" contains characters that aren't allowed.")
			return strings.ToValidUTF8(raw, "")
		case required && s == "":
			f.fail(field, label+" is required.")
		case s != "" && n < minLen:
			f.fail(field, label+" is too short.")
		case n > maxLen:
			f.fail(field, label+" is too long (at most "+strconv.Itoa(maxLen)+" characters).")
		}
		return s
	}
	f.Title = text("title", true, minTitle, maxTitle, true, "The summary")
	f.Description = text("description", false, minDescription, maxDescription, true, "The description")
	f.Steps = text("steps", false, 0, maxSteps, false, "Steps to reproduce")
	f.Version = text("version", true, 0, maxVersion, false, "Version and system")
	f.Name = text("name", true, 0, maxName, false, "Your name")
	f.Email = text("email", true, 0, maxEmail, false, "The e-mail address")
	f.ContactCode = strings.Join(strings.Fields(v.Get("contact_code")), "")
	if f.ContactCode == "" {
		f.fail("contact_code", "Your contact code is required: we reply by direct message.")
	} else if !ValidContactCode(f.ContactCode) {
		f.fail("contact_code", "That isn't a contact code. In the app, click the card button next to your name and copy your permanent code; it starts with "+PermanentCodePrefix)
		if len(f.ContactCode) > MaxContactCode {
			f.ContactCode = f.ContactCode[:MaxContactCode]
		}
		f.ContactCode = strings.ToValidUTF8(f.ContactCode, "")
	}
	if f.Email != "" {
		if a, err := mail.ParseAddress(f.Email); err != nil || a.Address != f.Email {
			f.fail("email", "That isn't an e-mail address (leave it empty if you don't want to give one).")
		}
	}
	if kind == "security" {
		f.Severity = v.Get("severity")
		if !oneOf(f.Severity, Severities) {
			f.Severity = ""
			f.fail("severity", "Choose a severity from the list.")
		}
	}
	return f
}

func (f *reportForm) content() ReportContent {
	return ReportContent{Title: f.Title, Description: f.Description, Steps: f.Steps, Version: f.Version,
		ContactCode: f.ContactCode, Name: f.Name, Email: f.Email, Severity: f.Severity}
}
