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
	maxCommunity   = 200
	maxName        = 64
	maxEmail       = 254
	// maxFormBytes bounds a form post's body.
	maxFormBytes = 128 << 10
)

// UserIDLen is the length of an OpenArcanum user ID: a 32-byte Ed25519
// public key in unpadded base64url, as the desktop app's "Copy user ID"
// gives it (client keys.encode; a member key in a community, or a DM
// identity key).
const UserIDLen = 43

// ValidUserID reports whether s is a user ID in its canonical form.
func ValidUserID(s string) bool {
	if len(s) != UserIDLen {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == 32
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
	UserID      string
	Community   string
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
	f.Community = text("community", true, 0, maxCommunity, true, "The community")
	f.Name = text("name", true, 0, maxName, false, "Your name")
	f.Email = text("email", true, 0, maxEmail, false, "The e-mail address")
	f.UserID = strings.TrimSpace(v.Get("user_id"))
	if f.UserID == "" {
		f.fail("user_id", "Your user ID is required: we contact you by direct message.")
	} else if !ValidUserID(f.UserID) {
		f.fail("user_id", "That isn't a user ID. In the app, right-click your name and choose Copy user ID; it is 43 letters, digits, - and _.")
		if len(f.UserID) > 100 {
			f.UserID = f.UserID[:100]
		}
		f.UserID = strings.ToValidUTF8(f.UserID, "")
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
		UserID: f.UserID, Community: f.Community, Name: f.Name, Email: f.Email, Severity: f.Severity}
}
