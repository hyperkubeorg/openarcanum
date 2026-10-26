package community

import (
	"context"
	"crypto/tls"
	"errors"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Link previews (docs/operations.md "Link previews"; docs/threat-model.md
// "Link-preview fetcher (SSRF)").
//
// When a community turns previews on and the sender has Embed Links in the
// channel, the community server fetches the first link of a new message;
// clients never do, so linked sites never see members' addresses. The
// fetcher only talks to public addresses: every address a host name
// resolves to is checked before connecting (a host with any private,
// loopback, link-local or otherwise special address is refused), the
// connection goes to the checked address (no second lookup to rebind), and
// the socket's address is checked again at connect time. Redirects are
// followed at most 3 times, each one checked the same way.

const (
	maxPreviewRedirects = 3
	maxPreviewTitle     = 200
	maxPreviewDesc      = 500
	maxPreviewSite      = 100
	maxPreviewURL       = 2048
	// Concurrent fetches; links beyond this are skipped, not queued.
	previewConcurrency = 4
)

var (
	errPreviewBlocked   = errors.New("address not allowed")
	errPreviewScheme    = errors.New("scheme not allowed")
	errPreviewRedirects = errors.New("too many redirects")
	errPreviewStatus    = errors.New("not a 200 response")
	errPreviewNotHTML   = errors.New("not text/html")
	errPreviewEmpty     = errors.New("no title or description")
)

// blockedPrefixes are special-purpose ranges netip's predicates don't cover.
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, broadcast
		"64:ff9b::/96",    // NAT64 (could reach private IPv4)
		"64:ff9b:1::/48",  // local-use NAT64
		"100::/64",        // discard
		"2001::/32",       // Teredo
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4 (embeds IPv4)
		"fec0::/10",       // site-local (deprecated)
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// publicAddr reports whether the fetcher may connect to ip.
func publicAddr(ip netip.Addr, allowLoopback bool) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	if ip.IsLoopback() {
		return allowLoopback
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

type previewFetcher struct {
	client        *http.Client
	dialer        *net.Dialer
	resolver      *net.Resolver
	schemes       map[string]bool
	pageMax       int64
	imageMax      int64
	timeout       time.Duration
	allowLoopback bool

	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
	wg     sync.WaitGroup
}

func newPreviewFetcher(cfg Config) *previewFetcher {
	f := &previewFetcher{
		resolver:      net.DefaultResolver,
		schemes:       map[string]bool{},
		pageMax:       cfg.PreviewMaxBytes,
		imageMax:      cfg.PreviewImageMaxBytes,
		timeout:       cfg.PreviewTimeout.Duration,
		allowLoopback: cfg.PreviewAllowLoopback,
		sem:           make(chan struct{}, previewConcurrency),
	}
	for _, s := range cfg.PreviewSchemes {
		f.schemes[s] = true
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.dialer = &net.Dialer{Timeout: f.timeout, Control: f.control}
	f.client = &http.Client{
		Timeout: f.timeout,
		Transport: &http.Transport{
			// Never a proxy from the environment: the checks below must
			// apply to the address actually connected to.
			Proxy:                  nil,
			DialContext:            f.dial,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    f.timeout,
			ResponseHeaderTimeout:  f.timeout,
			MaxResponseHeaderBytes: 64 << 10,
			DisableKeepAlives:      true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxPreviewRedirects {
				return errPreviewRedirects
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

// control re-checks the address of every socket at connect time.
func (f *previewFetcher) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errPreviewBlocked
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip, f.allowLoopback) {
		return errPreviewBlocked
	}
	return nil
}

// dial resolves the host, refuses it if any of its addresses isn't public,
// and connects to the checked addresses only.
func (f *previewFetcher) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		if ips, err = f.resolver.LookupNetIP(ctx, "ip", host); err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, errPreviewBlocked
	}
	for _, ip := range ips {
		if !publicAddr(ip, f.allowLoopback) {
			return nil, errPreviewBlocked
		}
	}
	var last error
	for _, ip := range ips {
		c, err := f.dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func (f *previewFetcher) checkURL(u *url.URL) error {
	if !f.schemes[u.Scheme] || u.Host == "" || u.User != nil || len(u.String()) > maxPreviewURL {
		return errPreviewScheme
	}
	return nil
}

// run starts a fetch job unless previewConcurrency jobs are already running
// (then the link gets no preview).
func (f *previewFetcher) run(job func(ctx context.Context)) {
	select {
	case f.sem <- struct{}{}:
	default:
		return
	}
	f.wg.Add(1)
	go func() {
		defer func() { <-f.sem; f.wg.Done() }()
		ctx, cancel := context.WithTimeout(f.ctx, 2*f.timeout+time.Second)
		defer cancel()
		job(ctx)
	}()
}

// wait cancels running fetches and waits for them (Server.Close).
func (f *previewFetcher) wait() {
	f.cancel()
	f.wg.Wait()
}

type previewResult struct {
	url, title, description, site string
	image                         []byte
	imageMime                     string
}

func (f *previewFetcher) get(ctx context.Context, u *url.URL, accept string) (*http.Response, error) {
	if err := f.checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OpenArcanumLinkPreview/1")
	req.Header.Set("Accept", accept)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, errPreviewStatus
	}
	return resp, nil
}

// fetch builds a preview for raw: the page (text/html only, at most pageMax
// bytes read) and its og:image (at most imageMax bytes, PNG/JPEG/GIF/WebP).
func (f *previewFetcher) fetch(ctx context.Context, raw string) (*previewResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errPreviewScheme
	}
	resp, err := f.get(ctx, u, "text/html")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "text/html" {
		return nil, errPreviewNotHTML
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, f.pageMax))
	if err != nil {
		return nil, err
	}
	og := parseOG(page)
	res := &previewResult{
		url:         raw,
		title:       cleanText(firstNonEmpty(og.title, og.htmlTitle), maxPreviewTitle),
		description: cleanText(firstNonEmpty(og.description, og.metaDescription), maxPreviewDesc),
		site:        cleanText(og.site, maxPreviewSite),
	}
	if res.title == "" && res.description == "" {
		return nil, errPreviewEmpty
	}
	if og.image != "" && f.imageMax > 0 {
		if iu, err := resp.Request.URL.Parse(og.image); err == nil {
			res.image, res.imageMime = f.fetchImage(ctx, iu)
		}
	}
	return res, nil
}

// fetchImage returns the image, or nil when it's missing, too large or not
// an image (the preview is kept without it).
func (f *previewFetcher) fetchImage(ctx context.Context, u *url.URL) ([]byte, string) {
	resp, err := f.get(ctx, u, "image/*")
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.imageMax+1))
	if err != nil || int64(len(data)) > f.imageMax || !IsImage(data) {
		return nil, ""
	}
	return data, http.DetectContentType(data)
}

// previewReason is a log-safe reason (never the URL, which is content).
func previewReason(err error) string {
	for _, known := range []error{errPreviewBlocked, errPreviewScheme, errPreviewRedirects, errPreviewStatus, errPreviewNotHTML, errPreviewEmpty} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "fetch failed"
}

// schedulePreview fetches a preview for a new or edited message in the
// background, if the community and channel allow previews and the author
// has Embed Links there. Callers hold s.mu.
func (s *Server) schedulePreview(author *memberRec, c *channelRec, m *wire.Message) {
	if s.previews == nil || m.Text == "" || m.System != nil {
		return
	}
	link := firstLink(m.Text)
	if link == "" || !s.previewsAllowed(c) || s.channelPerms(author, c)&PermEmbedLinks == 0 {
		return
	}
	id, channelID := m.Id, c.ID
	s.previews.run(func(ctx context.Context) {
		res, err := s.previews.fetch(ctx, link)
		if err != nil {
			s.log.Debug("link preview skipped", "message", id, "reason", previewReason(err))
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		c := s.channels[channelID]
		if c == nil || !s.previewsAllowed(c) {
			return
		}
		var imageID int64
		if res.image != nil {
			// A full file store keeps the preview, without its image.
			if ok, err := s.fileStoreHasRoom(ctx, storedSize(uint64(len(res.image)))); err != nil || !ok {
				res.image = nil
			}
		}
		if res.image != nil {
			ext := strings.TrimPrefix(res.imageMime, "image/")
			if imageID, err = s.storeFile(ctx, channelID, id, "preview."+ext, res.imageMime, nil, res.image); err != nil {
				s.log.Error("storing a link preview image failed", "message", id, "err", err)
				imageID = 0
			}
		}
		used := false
		err = s.updatePreview(ctx, id, func(body *wire.Message) bool {
			if firstLink(body.Text) != link {
				return false // edited since; the edit schedules its own preview
			}
			body.Preview = &wire.LinkPreview{Url: link, Title: res.title, Description: res.description, SiteName: res.site}
			if imageID != 0 {
				body.Preview.Image = &wire.FileRef{FileId: imageID}
			}
			used = true
			return true
		})
		if err != nil && !errors.Is(err, errNoMessage) {
			var re *rpcError
			if !errors.As(err, &re) {
				s.log.Error("saving a link preview failed", "message", id, "err", err)
			}
		}
		if !used && imageID != 0 {
			s.bestEffort(ctx, `DELETE FROM files WHERE id = ?`, imageID)
		}
	})
}

// ---------------------------------------------------------------- links in text

var (
	linkRE    = regexp.MustCompile("https?://[^\\s<>\"'`|]+")
	spoilerRE = regexp.MustCompile(`(?s)\|\|.*?\|\|`)
)

// firstLink is the first http(s) link in text outside code and spoilers,
// skipping links written as <https://…> (which, as in Discord, suppresses
// the preview). Trailing punctuation isn't part of the link.
func firstLink(text string) string {
	blank := func(m string) string { return strings.Repeat(" ", len(m)) }
	masked := spoilerRE.ReplaceAllStringFunc(codeBlockRE.ReplaceAllStringFunc(text, blank), blank)
	for _, loc := range linkRE.FindAllStringIndex(masked, -1) {
		start, end := loc[0], loc[1]
		link := strings.TrimRight(masked[start:end], ".,;:!?)]}*_~")
		if start > 0 && masked[start-1] == '<' && start+len(link) < len(masked) && masked[start+len(link)] == '>' {
			continue
		}
		if len(link) > len("https://") && len(link) <= maxPreviewURL {
			return link
		}
	}
	return ""
}

// ---------------------------------------------------------------- Open Graph tags

type ogData struct {
	title, description, site, image string
	htmlTitle, metaDescription      string
}

// parseOG reads og:title, og:description, og:site_name and og:image (plus
// <title> and meta description as fallbacks) from the head of an HTML page.
// It's a small tolerant scanner, not an HTML parser: it never builds a
// tree, stops at </head> or <body, and skips comments, scripts and styles.
func parseOG(page []byte) ogData {
	var d ogData
	low := asciiLower(page)
	s := string(page)
	i := 0
	for i < len(low) {
		j := indexFrom(low, "<", i)
		if j < 0 {
			break
		}
		rest := low[j:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			k := indexFrom(low, "-->", j+4)
			if k < 0 {
				return d
			}
			i = k + 3
		case strings.HasPrefix(rest, "<script"), strings.HasPrefix(rest, "<style"):
			tag := "</script"
			if strings.HasPrefix(rest, "<style") {
				tag = "</style"
			}
			k := indexFrom(low, tag, j+1)
			if k < 0 {
				return d
			}
			i = k + len(tag)
		case strings.HasPrefix(rest, "</head"), strings.HasPrefix(rest, "<body"):
			return d
		case strings.HasPrefix(rest, "<title") && len(rest) > 6 && (rest[6] == '>' || isSpace(rest[6])):
			k := indexFrom(low, ">", j)
			if k < 0 {
				return d
			}
			e := indexFrom(low, "</title", k+1)
			if e < 0 {
				return d
			}
			if d.htmlTitle == "" {
				d.htmlTitle = s[k+1 : e]
			}
			i = e + 7
		case strings.HasPrefix(rest, "<meta") && len(rest) > 5 && (isSpace(rest[5]) || rest[5] == '/'):
			attrs, end := parseAttrs(s, low, j+5)
			key := attrs["property"]
			if key == "" {
				key = attrs["name"]
			}
			content := attrs["content"]
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "og:title":
				d.title = keepFirst(d.title, content)
			case "og:description":
				d.description = keepFirst(d.description, content)
			case "og:site_name":
				d.site = keepFirst(d.site, content)
			case "og:image", "og:image:url", "og:image:secure_url":
				d.image = keepFirst(d.image, strings.TrimSpace(html.UnescapeString(content)))
			case "description":
				d.metaDescription = keepFirst(d.metaDescription, content)
			}
			i = end
		default:
			i = j + 1
		}
	}
	return d
}

// parseAttrs reads name=value pairs from i up to the closing '>'. s and low
// are the same text (low ASCII-lowercased, so byte offsets match).
func parseAttrs(s, low string, i int) (map[string]string, int) {
	attrs := map[string]string{}
	for i < len(s) {
		for i < len(s) && (isSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) || s[i] == '>' {
			return attrs, i + 1
		}
		start := i
		for i < len(s) && !isSpace(s[i]) && s[i] != '=' && s[i] != '>' && s[i] != '/' {
			i++
		}
		name := low[start:i]
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		val := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && isSpace(s[i]) {
				i++
			}
			if i < len(s) && (s[i] == '"' || s[i] == '\'') {
				q := s[i]
				end := strings.IndexByte(s[i+1:], q)
				if end < 0 {
					return attrs, len(s)
				}
				val = s[i+1 : i+1+end]
				i += end + 2
			} else {
				vs := i
				for i < len(s) && !isSpace(s[i]) && s[i] != '>' {
					i++
				}
				val = s[vs:i]
			}
		}
		if name != "" {
			if _, dup := attrs[name]; !dup {
				attrs[name] = val
			}
		}
		if start == i {
			i++
		}
	}
	return attrs, len(s)
}

func asciiLower(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func indexFrom(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	k := strings.Index(s[from:], sub)
	if k < 0 {
		return -1
	}
	return from + k
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func keepFirst(cur, v string) string {
	if cur != "" {
		return cur
	}
	return v
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// cleanText unescapes HTML entities, drops control characters, collapses
// whitespace and truncates to max characters.
func cleanText(v string, max int) string {
	v = html.UnescapeString(v)
	if !utf8.ValidString(v) {
		v = strings.ToValidUTF8(v, "")
	}
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, v)
	v = strings.Join(strings.Fields(v), " ")
	return truncate(v, max)
}
