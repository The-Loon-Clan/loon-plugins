package pluginapi

import (
	"errors"
	"net/url"
	"strings"
)

// Credential redaction for outbound-request errors.
//
// THE PROBLEM, demonstrated rather than assumed. net/http returns a *url.Error
// on any transport failure, and its message embeds the FULL URL:
//
//	Get "https://api.themoviedb.org/3/search/movie?api_key=abc123&query=hi":
//	  dial tcp: lookup api.themoviedb.org: no such host
//
// A plugin that does the obvious thing —
//
//	return fmt.Errorf("tmdb request: %w", err)
//
// has now put the operator's API key into whatever reads that error. On this
// host that is core.Errors.Report, which writes a row to error_logs and renders
// it on an admin page. One DNS blip is all it takes, and nothing about the
// failure hints that a credential went with it.
//
// It is a quiet leak because the credential is in the URL for a reason the
// author is thinking about (the API requires it) and lands in the error for a
// reason nobody is thinking about (the transport is being helpful).
//
// WHY NOT JUST DROP THE QUERY. The query also carries the thing being searched
// for, which is most of the diagnostic value — an error saying a lookup for
// "The Office" timed out is useful, one saying a lookup timed out is not. So
// the parameters that look like credentials are replaced and the rest survive.

// secretParam reports whether a query parameter's name suggests a credential.
//
// Matched on a SUBSTRING and case-insensitively, because the alternative is a
// list that is correct until the next API uses a name nobody thought of.
// Over-redacting a parameter called "monkey" costs a word in a log line;
// under-redacting costs the credential.
func secretParam(name string) bool {
	n := strings.ToLower(name)
	for _, needle := range []string{
		"key", "token", "secret", "pass", "auth", "sig", "client", "credential", "session",
	} {
		if strings.Contains(n, needle) {
			return true
		}
	}
	return false
}

// RedactSecrets rewrites a URL's credential-looking query parameters.
//
// The URL is returned unchanged when it cannot be parsed — a mangled string is
// not worth failing over, and the caller's error is about something else.
func RedactSecrets(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	changed := false
	for name := range q {
		if secretParam(name) {
			q.Set(name, "REDACTED")
			changed = true
		}
	}
	// Userinfo carries credentials too, and it is easy to forget because it is
	// not part of the query at all.
	if u.User != nil {
		u.User = url.UserPassword("REDACTED", "REDACTED")
		changed = true
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RedactURLError returns err with any embedded URL's credentials removed.
//
// Use it on EVERY error out of an http.Client, not only the ones from requests
// known to carry a credential: a redirect can move a request to a URL the
// caller never built, and the check costs nothing when there is nothing to
// redact.
//
//	resp, err := s.http.Do(req)
//	if err != nil {
//	    return fmt.Errorf("tmdb request: %w", pluginapi.RedactURLError(err))
//	}
//
// A *url.Error is rebuilt with the same Op and Err so errors.Is and errors.As
// keep working — including on the wrapped cause, which is what callers actually
// test (context.DeadlineExceeded, net.Error and so on).
func RedactURLError(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	safe := RedactSecrets(ue.URL)
	if safe == ue.URL {
		return err
	}
	return &url.Error{Op: ue.Op, URL: safe, Err: ue.Err}
}
