package community

import (
	"context"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// AutoMod (docs/protocol.md §4.8): rules checked on the server for every
// post, edit and file caption. Matching runs on the
// message text in memory and never logs it; alerts reference message ids.
//
// Regular expressions are Go RE2 (regexp), which matches in time linear in
// the input, so a pattern can't backtrack catastrophically. What's left is
// the size of the work, which these limits bound: the program size of every
// pattern together (maxPatternInsts) times the message length
// (MaxMessageChars) is the most a message can cost (docs/threat-model.md).
const (
	maxAutoModRules    = 20
	maxKeywords        = 1000
	maxKeywordLen      = 60
	maxPatterns        = 10
	maxPatternLen      = 260
	maxPatternInsts    = 5000 // compiled RE2 instructions, all rules together
	maxPatternInstsOne = 1000 // and per pattern
	maxAllowedDomains  = 100
	maxMentionLimit    = 50
	maxAutoModExempt   = 50
	maxAutoModNameLen  = 64
	automodTimeoutCap  = MaxTimeoutSeconds
	automodTimeoutMins = MinTimeoutSeconds
)

// autoModRec is a stored rule (sealed JSON in automod_rules.data).
type autoModRec struct {
	Name           string              `json:"name"`
	Enabled        bool                `json:"enabled"`
	Trigger        wire.AutoModTrigger `json:"trigger"`
	Keywords       []string            `json:"keywords,omitempty"`
	Patterns       []string            `json:"patterns,omitempty"`
	MentionLimit   uint32              `json:"mention_limit,omitempty"`
	AllowedDomains []string            `json:"allowed_domains,omitempty"`
	Block          bool                `json:"block"`
	Alert          bool                `json:"alert"`
	TimeoutSeconds uint32              `json:"timeout_seconds,omitempty"`
	ExemptRoles    []int64             `json:"exempt_roles,omitempty"`
	ExemptChannels []int64             `json:"exempt_channels,omitempty"`
}

func (r *autoModRec) proto(id int64) *wire.AutoModRule {
	return &wire.AutoModRule{
		Id: id, Name: r.Name, Enabled: r.Enabled, Trigger: r.Trigger, Keywords: r.Keywords, Patterns: r.Patterns,
		MentionLimit: r.MentionLimit, AllowedDomains: r.AllowedDomains, Block: r.Block, Alert: r.Alert,
		TimeoutSeconds: r.TimeoutSeconds, ExemptRoleIds: r.ExemptRoles, ExemptChannelIds: r.ExemptChannels,
	}
}

// keyword is one normalized keyword: its words joined by single spaces, and
// whether it matches as a prefix (word*) and/or suffix (*word).
type keyword struct {
	text           string
	prefix, suffix bool
}

type compiledRule struct {
	id       int64
	rec      autoModRec
	keywords []keyword
	patterns []*regexp.Regexp
	insts    int
}

// autoModSet is the live rule set; the server swaps it whole under amu.
type autoModSet struct {
	rules []*compiledRule
}

func (a *autoModSet) insts(except int64) int {
	n := 0
	for _, r := range a.rules {
		if r.id != except {
			n += r.insts
		}
	}
	return n
}

// words splits text into lowercase letter/digit runs.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func compileKeyword(raw string) (keyword, error) {
	k := keyword{}
	t := strings.TrimSpace(raw)
	if strings.HasPrefix(t, "*") {
		k.suffix = true
		t = strings.TrimPrefix(t, "*")
	}
	if strings.HasSuffix(t, "*") {
		k.prefix = true
		t = strings.TrimSuffix(t, "*")
	}
	if strings.Contains(t, "*") {
		return k, fmt.Errorf("keyword %q: * is only allowed at the start or end", raw)
	}
	k.text = strings.Join(words(t), " ")
	if k.text == "" {
		return k, fmt.Errorf("keyword %q has no letters or digits", raw)
	}
	return k, nil
}

// compilePattern validates one regular expression at save time: length,
// syntax and compiled size. Patterns match case-insensitively.
func compilePattern(p string) (*regexp.Regexp, int, error) {
	if utf8.RuneCountInString(p) > maxPatternLen {
		return nil, 0, fmt.Errorf("pattern %q is longer than %d characters", truncate(p, 40), maxPatternLen)
	}
	src := "(?i)" + p
	parsed, err := syntax.Parse(src, syntax.Perl)
	if err != nil {
		return nil, 0, fmt.Errorf("pattern %q: %v", truncate(p, 40), err)
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil, 0, fmt.Errorf("pattern %q: %v", truncate(p, 40), err)
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, 0, fmt.Errorf("pattern %q: %v", truncate(p, 40), err)
	}
	if len(prog.Inst) > maxPatternInstsOne {
		return nil, 0, fmt.Errorf("pattern %q is too complex (shorten repetitions like {100})", truncate(p, 40))
	}
	if re.MatchString("") {
		return nil, 0, fmt.Errorf("pattern %q matches every message", truncate(p, 40))
	}
	return re, len(prog.Inst), nil
}

var domainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// normalizeRule validates a rule as an admin saves it and compiles it.
func (s *Server) normalizeRule(r *wire.AutoModRule) (*compiledRule, error) {
	rec := autoModRec{
		Name: strings.TrimSpace(r.Name), Enabled: r.Enabled, Trigger: r.Trigger,
		Block: r.Block, Alert: r.Alert, TimeoutSeconds: r.TimeoutSeconds,
	}
	if rec.Name == "" || utf8.RuneCountInString(rec.Name) > maxAutoModNameLen {
		return nil, errInvalid("rule names are 1 to 64 characters")
	}
	if !rec.Block && !rec.Alert && rec.TimeoutSeconds == 0 {
		return nil, errInvalid("a rule needs at least one action: block, alert or time out")
	}
	if rec.TimeoutSeconds != 0 && (rec.TimeoutSeconds < automodTimeoutMins || rec.TimeoutSeconds > automodTimeoutCap) {
		return nil, errInvalid("a timeout lasts from 60 seconds to 28 days")
	}
	cr := &compiledRule{id: r.Id}
	switch r.Trigger {
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD:
		if len(r.Keywords) > maxKeywords {
			return nil, errInvalid(fmt.Sprintf("a rule has at most %d keywords", maxKeywords))
		}
		if len(r.Patterns) > maxPatterns {
			return nil, errInvalid(fmt.Sprintf("a rule has at most %d regular expressions", maxPatterns))
		}
		seen := map[string]bool{}
		for _, raw := range r.Keywords {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			if utf8.RuneCountInString(raw) > maxKeywordLen {
				return nil, errInvalid(fmt.Sprintf("keywords are at most %d characters", maxKeywordLen))
			}
			k, err := compileKeyword(raw)
			if err != nil {
				return nil, errInvalid(err.Error())
			}
			norm := strings.TrimSpace(raw)
			if seen[norm] {
				continue
			}
			seen[norm] = true
			rec.Keywords = append(rec.Keywords, norm)
			cr.keywords = append(cr.keywords, k)
		}
		for _, p := range r.Patterns {
			if strings.TrimSpace(p) == "" {
				continue
			}
			re, n, err := compilePattern(p)
			if err != nil {
				return nil, errInvalid(err.Error())
			}
			rec.Patterns = append(rec.Patterns, p)
			cr.patterns = append(cr.patterns, re)
			cr.insts += n
		}
		if len(rec.Keywords) == 0 && len(rec.Patterns) == 0 {
			return nil, errInvalid("a keyword rule needs keywords or regular expressions")
		}
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM:
		if r.MentionLimit < 1 || r.MentionLimit > maxMentionLimit {
			return nil, errInvalid(fmt.Sprintf("the mention limit is between 1 and %d", maxMentionLimit))
		}
		rec.MentionLimit = r.MentionLimit
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS:
		if len(r.AllowedDomains) > maxAllowedDomains {
			return nil, errInvalid(fmt.Sprintf("at most %d allowed domains", maxAllowedDomains))
		}
		seen := map[string]bool{}
		for _, d := range r.AllowedDomains {
			d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "*.")
			if d == "" || seen[d] {
				continue
			}
			if len(d) > 253 || !domainRE.MatchString(d) {
				return nil, errInvalid(fmt.Sprintf("%q isn't a domain name (like example.org)", truncate(d, 60)))
			}
			seen[d] = true
			rec.AllowedDomains = append(rec.AllowedDomains, d)
		}
	default:
		return nil, errInvalid("choose what the rule looks for: keywords, mention spam or links")
	}
	if len(r.ExemptRoleIds) > maxAutoModExempt || len(r.ExemptChannelIds) > maxAutoModExempt {
		return nil, errInvalid(fmt.Sprintf("at most %d exempt roles and %d exempt channels", maxAutoModExempt, maxAutoModExempt))
	}
	for _, id := range r.ExemptRoleIds {
		if s.roles[id] == nil {
			return nil, errNotFound("an exempt role doesn't exist")
		}
		rec.ExemptRoles = append(rec.ExemptRoles, id)
	}
	for _, id := range r.ExemptChannelIds {
		if s.channels[id] == nil {
			return nil, errNotFound("an exempt channel doesn't exist")
		}
		rec.ExemptChannels = append(rec.ExemptChannels, id)
	}
	cr.rec = rec
	return cr, nil
}

// ---------------------------------------------------------------- matching

var (
	plainMentionRE = regexp.MustCompile(`(?:^|\s)@([\p{L}\p{N}_.\-]+)`)
	autoModLinkRE  = regexp.MustCompile(`(?i)(?:\b[a-z][a-z0-9+.\-]*://|\bwww\.)([^\s/?#<>"'\x60\\]+)`)
)

// mentionCount is the number of distinct @mentions in text: mention
// tokens (`<@member>`, `<@&role>`, docs/protocol.md §4.3) and typed @names
// (@everyone and @here count), outside code.
func mentionCount(text string) int {
	text = codeBlockRE.ReplaceAllString(text, " ")
	seen := map[string]bool{}
	for _, m := range mentionRE.FindAllStringSubmatch(text, -1) {
		if m[3] == "" {
			seen["<"+m[1]+m[2]] = true
		}
	}
	for _, m := range plainMentionRE.FindAllStringSubmatch(text, -1) {
		seen[strings.ToLower(strings.TrimRight(m[1], ".-"))] = true
	}
	return len(seen)
}

// linkHosts lists the hosts of the links in text (scheme://host or www.host).
func linkHosts(text string) []string {
	var out []string
	for _, m := range autoModLinkRE.FindAllStringSubmatchIndex(text, -1) {
		host := strings.ToLower(text[m[2]:m[3]])
		if strings.HasPrefix(strings.ToLower(text[m[0]:m[1]]), "www.") {
			host = "www." + host
		}
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		if strings.HasPrefix(host, "[") {
			if i := strings.Index(host, "]"); i >= 0 {
				host = host[:i+1]
			}
		} else if i := strings.Index(host, ":"); i >= 0 {
			host = host[:i]
		}
		out = append(out, strings.TrimRight(host, "."))
	}
	return out
}

func domainAllowed(host string, allowed []string) bool {
	for _, d := range allowed {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// match reports whether the rule fires on text, and a member-facing reason.
func (r *compiledRule) match(text string, normalized string, wordList []string) (bool, string) {
	switch r.rec.Trigger {
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD:
		for _, k := range r.keywords {
			if keywordMatch(k, normalized, wordList) {
				return true, "it contains a blocked word"
			}
		}
		for _, p := range r.patterns {
			if p.MatchString(text) {
				return true, "it matches a blocked pattern"
			}
		}
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM:
		if mentionCount(text) > int(r.rec.MentionLimit) {
			return true, fmt.Sprintf("it mentions more than %d people", r.rec.MentionLimit)
		}
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS:
		for _, h := range linkHosts(text) {
			if !domainAllowed(h, r.rec.AllowedDomains) {
				return true, "it links to a site that isn't allowed here"
			}
		}
	}
	return false, ""
}

// keywordMatch checks one keyword against the text's words: whole words
// or phrases by default, word prefixes with a trailing *, suffixes with a
// leading *, anywhere with both.
func keywordMatch(k keyword, normalized string, wordList []string) bool {
	switch {
	case k.prefix && k.suffix:
		return strings.Contains(normalized, k.text)
	case k.prefix:
		return strings.Contains(normalized, " "+k.text)
	case k.suffix:
		return strings.Contains(normalized, k.text+" ")
	}
	if !strings.Contains(k.text, " ") {
		for _, w := range wordList {
			if w == k.text {
				return true
			}
		}
		return false
	}
	return strings.Contains(normalized, " "+k.text+" ")
}

// autoModHit is one rule that fired.
type autoModHit struct {
	rule   *compiledRule
	reason string
}

// autoModCheck runs every enabled rule over text, without holding s.mu
// (it reads only the rule snapshot). Exemptions are applied afterwards by
// autoModApply, under s.mu.
func (s *Server) autoModCheck(text string) []autoModHit {
	s.amu.RLock()
	set := s.automod
	s.amu.RUnlock()
	if set == nil || len(set.rules) == 0 || text == "" {
		return nil
	}
	ws := words(text)
	normalized := " " + strings.Join(ws, " ") + " "
	var hits []autoModHit
	for _, r := range set.rules {
		if !r.rec.Enabled {
			continue
		}
		if ok, why := r.match(text, normalized, ws); ok {
			hits = append(hits, autoModHit{r, why})
		}
	}
	return hits
}

// autoModOutcome is what the server does about the rules that fired.
type autoModOutcome struct {
	blocked error
	alerts  []*compiledRule
	timeout uint32
	rules   []*compiledRule
}

// autoModFilter drops hits exempt for this member or channel and works
// out the actions. Callers hold s.mu.
func (s *Server) autoModFilter(hits []autoModHit, m *memberRec, channelID int64) *autoModOutcome {
	if len(hits) == 0 || m == nil {
		return nil
	}
	held := map[int64]bool{}
	for _, id := range m.Roles {
		held[id] = true
	}
	out := &autoModOutcome{}
	for _, h := range hits {
		exempt := false
		for _, id := range h.rule.rec.ExemptRoles {
			if held[id] || id == s.everyoneID {
				exempt = true
			}
		}
		for _, id := range h.rule.rec.ExemptChannels {
			if id == channelID {
				exempt = true
			}
		}
		if exempt {
			continue
		}
		out.rules = append(out.rules, h.rule)
		if h.rule.rec.Block && out.blocked == nil {
			out.blocked = errDenied(fmt.Sprintf("AutoMod blocked your message: %s (rule “%s”).", h.reason, h.rule.rec.Name))
		}
		if h.rule.rec.Alert {
			out.alerts = append(out.alerts, h.rule)
		}
		out.timeout = max(out.timeout, h.rule.rec.TimeoutSeconds)
	}
	if len(out.rules) == 0 {
		return nil
	}
	return out
}

// autoModAct records alerts and applies a timeout. messageID is 0 when the
// message was blocked. It returns what to do once s.mu is released.
// Callers hold s.mu for writing.
func (s *Server) autoModAct(ctx context.Context, o *autoModOutcome, m *memberRec, channelID, messageID int64) []func() {
	if o == nil {
		return nil
	}
	var after []func()
	action := "alerted"
	if o.blocked != nil {
		action = "blocked"
	}
	ev, roles := s.roleInfos()
	// Like a moderator's timeout, AutoMod's never applies to owners or
	// administrators (their message is still blocked and reported).
	if o.timeout > 0 && !s.isOwner(m) && BasePerms(s.subject(m), ev, roles)&PermAdministrator == 0 {
		until := s.now().UnixMilli() + int64(o.timeout)*1000
		if until > m.TimeoutUntil {
			next := *m
			next.TimeoutUntil = until
			if err := s.saveMember(ctx, s.db, &next); err != nil {
				s.log.Error("AutoMod timeout failed", "err", err)
			} else {
				*m = next
				action += ", timed out for " + humanSeconds(o.timeout)
				var names []string
				for _, r := range o.rules {
					if r.rec.TimeoutSeconds > 0 {
						names = append(names, "“"+r.rec.Name+"”")
					}
				}
				s.systemAudit(ctx, "automod", m.Key, fmt.Sprintf("AutoMod rule %s timed out %s for %s", strings.Join(names, ", "), s.nameOf(m.Key), humanSeconds(o.timeout)))
				after = append(after, s.broadcastState, func() { s.scheduleTimeoutEnd(until) })
			}
		}
	}
	for _, r := range o.alerts {
		// The same limits as member reports: the queue's open-report cap,
		// and a per-author rate, so one member tripping rules can't flood
		// the queue.
		open, err := s.count(ctx, `SELECT count(*) FROM reports WHERE status = ?`, int(wire.ReportStatus_REPORT_STATUS_OPEN))
		if err != nil {
			s.log.Error("AutoMod alert failed", "err", err)
			continue
		}
		if open >= maxOpenReports || !s.alertLimit.allowN(m.Key, s.now(), alertBurst, reportWindow) {
			continue
		}
		rep, err := s.createReport(ctx, &reportRec{
			Kind: wire.ReportKind_REPORT_KIND_AUTOMOD, CreatedAt: s.now().UnixMilli(), MessageID: messageID,
			ChannelID: channelID, Author: m.Key, RuleID: r.id, RuleName: r.rec.Name, AutoModAction: action,
		}, "", nil)
		if err != nil {
			s.log.Error("AutoMod alert failed", "err", err)
			continue
		}
		after = append(after, s.modAlertSender(rep))
	}
	return after
}

// ---------------------------------------------------------------- storage and admin ops

func (s *Server) loadAutoMod(ctx context.Context) error {
	set := &autoModSet{}
	err := s.loadRows(ctx, `SELECT id, data FROM automod_rules ORDER BY id`, func(id int64, data []byte) error {
		var rec autoModRec
		if err := s.openJSON("automod_rules", "data", rowID(id), data, &rec); err != nil {
			return err
		}
		cr := &compiledRule{id: id, rec: rec}
		for _, k := range rec.Keywords {
			kw, err := compileKeyword(k)
			if err != nil {
				return &TamperError{"automod_rules", "data", rowID(id)}
			}
			cr.keywords = append(cr.keywords, kw)
		}
		for _, p := range rec.Patterns {
			re, n, err := compilePattern(p)
			if err != nil {
				return &TamperError{"automod_rules", "data", rowID(id)}
			}
			cr.patterns = append(cr.patterns, re)
			cr.insts += n
		}
		set.rules = append(set.rules, cr)
		return nil
	})
	if err != nil {
		return err
	}
	s.amu.Lock()
	s.automod = set
	s.amu.Unlock()
	return nil
}

func (s *Server) autoModRules() *autoModSet {
	s.amu.RLock()
	defer s.amu.RUnlock()
	if s.automod == nil {
		return &autoModSet{}
	}
	return s.automod
}

func (s *Server) opSaveAutoModRule(ctx context.Context, actor *memberRec, sr *wire.SaveAutoModRule, out *adminOutcome) error {
	if err := s.need(actor, PermManageCommunity); err != nil {
		return err
	}
	if sr.Rule == nil {
		return errInvalid("no rule")
	}
	cr, err := s.normalizeRule(sr.Rule)
	if err != nil {
		return err
	}
	cur := s.autoModRules()
	verb := "edited"
	if cr.id == 0 {
		if len(cur.rules) >= maxAutoModRules {
			return errConflict(fmt.Sprintf("a community has at most %d AutoMod rules", maxAutoModRules))
		}
		id, err := s.nextID(ctx, s.db, "automod_rules")
		if err != nil {
			return err
		}
		cr.id, verb = id, "created"
	} else {
		found := false
		for _, r := range cur.rules {
			found = found || r.id == cr.id
		}
		if !found {
			return errNotFound("no such AutoMod rule")
		}
	}
	if cur.insts(cr.id)+cr.insts > maxPatternInsts {
		return errInvalid("these regular expressions are too complex together; simplify or remove some")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO automod_rules (id, data) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`,
		cr.id, s.sealJSON("automod_rules", "data", rowID(cr.id), &cr.rec)); err != nil {
		return err
	}
	next := &autoModSet{}
	replaced := false
	for _, r := range cur.rules {
		if r.id == cr.id {
			next.rules = append(next.rules, cr)
			replaced = true
		} else {
			next.rules = append(next.rules, r)
		}
	}
	if !replaced {
		next.rules = append(next.rules, cr)
	}
	sort.Slice(next.rules, func(i, j int) bool { return next.rules[i].id < next.rules[j].id })
	s.amu.Lock()
	s.automod = next
	s.amu.Unlock()
	out.result.Id = cr.id
	state := "enabled"
	if !cr.rec.Enabled {
		state = "disabled"
	}
	out.summary = fmt.Sprintf("%s AutoMod rule “%s” (%s, %s)", verb, cr.rec.Name, triggerName(cr.rec.Trigger), state)
	return nil
}

func triggerName(t wire.AutoModTrigger) string {
	switch t {
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD:
		return "keywords"
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM:
		return "mention spam"
	case wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS:
		return "links"
	}
	return "unknown"
}

func (s *Server) opDeleteAutoModRule(ctx context.Context, actor *memberRec, d *wire.DeleteAutoModRule, out *adminOutcome) error {
	if err := s.need(actor, PermManageCommunity); err != nil {
		return err
	}
	cur := s.autoModRules()
	next := &autoModSet{}
	var gone *compiledRule
	for _, r := range cur.rules {
		if r.id == d.Id {
			gone = r
		} else {
			next.rules = append(next.rules, r)
		}
	}
	if gone == nil {
		return errNotFound("no such AutoMod rule")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM automod_rules WHERE id = ?`, d.Id); err != nil {
		return err
	}
	s.amu.Lock()
	s.automod = next
	s.amu.Unlock()
	out.summary = fmt.Sprintf("deleted AutoMod rule “%s”", gone.rec.Name)
	return nil
}

func (s *Server) listAutoModRules(ss *sess) (*wire.ServerMessage, error) {
	s.mu.RLock()
	allowed := s.basePerms(s.members[string(ss.member)])&PermManageCommunity != 0
	s.mu.RUnlock()
	if !allowed {
		return nil, errDenied("you can't see the AutoMod rules")
	}
	out := &wire.AutoModRuleList{}
	for _, r := range s.autoModRules().rules {
		out.Rules = append(out.Rules, r.rec.proto(r.id))
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_AutomodRules{AutomodRules: out}}, nil
}
