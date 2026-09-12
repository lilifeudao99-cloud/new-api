package common

import (
	"net/url"
	"regexp"
	"strings"
)

var absoluteHTTPURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func RewriteExternalErrorURLs(message, requestHost, requestScheme string) string {
	host := strings.TrimSpace(requestHost)
	if host == "" {
		host = "ailili.chat"
	}
	if requestScheme != "http" && requestScheme != "https" {
		requestScheme = "https"
	}
	return absoluteHTTPURLPattern.ReplaceAllStringFunc(message, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || isAililiHost(parsed.Hostname()) {
			return raw
		}
		parsed.Scheme = requestScheme
		parsed.Host = host
		return parsed.String()
	})
}

func isAililiHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "ailili.chat" || strings.HasSuffix(host, ".ailili.chat")
}
