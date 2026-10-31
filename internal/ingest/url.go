package ingest

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

var activityID = regexp.MustCompile(`(?:activity[-:]|share-)(\d{15,22})`)

func IsLinkedIn(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "linkedin.com" || strings.HasSuffix(host, ".linkedin.com")
}

// ValidateURL also protects the import/source endpoints from fetching local
// services. DNS and redirect destinations are checked again by HTTPFetcher.
func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("enter an absolute http or https URL")
	}
	if u.User != nil {
		return nil, errors.New("URLs containing credentials are not supported")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return nil, errors.New("sources must use a public internet address")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !PublicIP(ip) {
		return nil, errors.New("private and local network addresses are not supported")
	}
	return u, nil
}

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, block := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	return true
}

func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	q := u.Query()
	for key := range q {
		if strings.HasPrefix(strings.ToLower(key), "utm_") || key == "trk" || key == "rcm" || key == "fbclid" || key == "gclid" || key == "ref" {
			q.Del(key)
		}
	}
	if IsLinkedIn(u.Hostname()) {
		u.Host, u.Scheme, u.RawQuery = "www.linkedin.com", "https", ""
	} else {
		u.RawQuery = q.Encode()
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// UnwrapLink extracts the destination from LinkedIn's public redirect links.
// It does not follow the link or sign into LinkedIn.
func UnwrapLink(raw, base string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if b, err := url.Parse(base); err == nil {
		u = b.ResolveReference(u)
	}
	if IsLinkedIn(u.Hostname()) && strings.HasPrefix(u.Path, "/redir/") {
		if target := u.Query().Get("url"); target != "" {
			u, err = url.Parse(target)
			if err != nil {
				return ""
			}
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.User != nil {
		return ""
	}
	return u.String()
}

func LinkedInIdentity(raw string) string {
	matches := activityID.FindStringSubmatch(raw)
	if len(matches) > 1 {
		return "linkedin-" + matches[1]
	}
	return ""
}

func SourceKind(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && IsLinkedIn(u.Hostname()) {
		if strings.HasPrefix(u.Path, "/posts/") || strings.Contains(u.Path, "/feed/update/") {
			return "linkedin_post"
		}
		return "linkedin_page"
	}
	if strings.Contains(strings.ToLower(raw), "/feed") || strings.HasSuffix(strings.ToLower(raw), ".xml") || strings.HasSuffix(strings.ToLower(raw), ".rss") {
		return "rss"
	}
	return "web_page"
}
