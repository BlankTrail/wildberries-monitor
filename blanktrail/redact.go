// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"strconv"
	"strings"
)

// redacted is what a hidden credential reads as.
const redacted = "***"

// Redact hides the credentials in anything that names a way out: a proxy list
// line in any form Parse accepts, the address a list is fetched from, a
// provider's rotate link.
//
// It lives beside Parse because Parse is what decides where a credential sits
// in each of those forms, and a second copy of that decision somewhere else is
// one that drifts the day a sixth form is accepted here.
//
// What it hides:
//
//   - the password in user:pass@host, with or without a scheme in front;
//   - the password in the bare host:port:user:pass form;
//   - the value of a query parameter whose name says it is a secret
//     (key, token, secret, pass, sig, auth, hash and spellings around them).
//
// What it keeps: the user name, the host, the port and the rest of the
// address. The user name is half of what says which of a provider's accounts
// this is, and masking it would make two channels look identical.
//
// What it cannot see: a token written into a link's path, such as
// /rotate/9f8e7d. Nothing about a path segment says it is a secret, and
// guessing would garble every address that has an ordinary path.
//
// The input's spelling is kept everywhere it does not hide something. It is
// not re-encoded through net/url, which would percent-escape the mask itself
// and change characters the person typed.
func Redact(s string) string {
	if s == "" {
		return s
	}
	prefix, rest := "", s
	if i := strings.Index(s, "://"); i >= 0 {
		prefix, rest = s[:i+3], s[i+3:]
	}

	// The authority is what comes before the first slash, question mark or
	// fragment. Only there does an "@" separate credentials from a host; one
	// in a path or a query is part of something else.
	authority, tail := rest, ""
	if cut := strings.IndexAny(rest, "/?#"); cut >= 0 {
		authority, tail = rest[:cut], rest[cut:]
	}

	switch at := strings.LastIndex(authority, "@"); {
	case at >= 0:
		authority = maskUserinfo(authority[:at]) + authority[at:]
	case prefix == "" && tail == "":
		authority = maskBareColonForm(authority)
	}
	return prefix + authority + redactQuery(tail)
}

// maskUserinfo is user:*** for user:pass, and the input unchanged for a user
// with no password — there is nothing to hide, and blanking the name would
// lose which account it is.
func maskUserinfo(userinfo string) string {
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return userinfo
	}
	return userinfo[:colon+1] + redacted
}

// maskBareColonForm hides everything after the user in host:port:user:pass.
//
// Everything, not only the fourth field: a password with a colon in it makes
// five fields, Parse refuses that line, and the refusal is exactly where it is
// quoted back to a person. Only when the second field is a port, though — a
// Windows path such as C:\lists\a.txt has a colon too, and fields with no port
// among them are not this form.
func maskBareColonForm(s string) string {
	parts := strings.Split(s, ":")
	if len(parts) < 4 {
		return s
	}
	if n, err := strconv.Atoi(parts[1]); err != nil || n < 1 || n > 65535 {
		return s
	}
	return parts[0] + ":" + parts[1] + ":" + parts[2] + ":" + redacted
}

// redactQuery masks the values of secret-looking parameters in a path's query
// string, leaving the path, the other parameters and the fragment as written.
func redactQuery(tail string) string {
	q := strings.Index(tail, "?")
	if q < 0 {
		return tail
	}
	path, query, fragment := tail[:q+1], tail[q+1:], ""
	if h := strings.Index(query, "#"); h >= 0 {
		query, fragment = query[:h], query[h:]
	}
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		name, _, hasValue := strings.Cut(pair, "=")
		if hasValue && secretName(name) {
			pairs[i] = name + "=" + redacted
		}
	}
	return path + strings.Join(pairs, "&") + fragment
}

// secretWords are what a parameter name contains when its value is a
// credential. Substrings rather than whole names, because providers spell the
// same thing api_key, apiKey, x-api-key and key alike.
var secretWords = []string{"key", "token", "secret", "pass", "pwd", "sig", "auth", "hash"}

func secretName(name string) bool {
	name = strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}
