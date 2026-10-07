// SPDX-License-Identifier: AGPL-3.0-or-later

package blanktrail

import (
	"strings"
	"testing"
)

func TestRedact_HidesTheCredentialAndKeepsTheAddressReadable(t *testing.T) {
	// Whatever names a way out ends up in a table, an error or a test summary,
	// and those are what get screenshotted into a support chat. The user name
	// stays: it is half of what says which of a provider's accounts this is.
	for _, c := range []struct{ in, want string }{
		// Every line form Parse accepts that can carry a password.
		{"socks5://user:pass@10.0.0.1:1080", "socks5://user:***@10.0.0.1:1080"},
		{"user:pass@10.0.0.1:1080", "user:***@10.0.0.1:1080"},
		{"10.0.0.1:1080:user:pass", "10.0.0.1:1080:user:***"},
		// A password with a colon in it: five fields, which Parse refuses — and
		// a refused line is the one that gets quoted back.
		{"10.0.0.1:1080:user:pa:ss", "10.0.0.1:1080:user:***"},
		{"socks5://user:pass@[2001:db8::1]:1080", "socks5://user:***@[2001:db8::1]:1080"},

		// A list's own address, and a provider's rotate link.
		{"http://u:p@host:8080/list.txt", "http://u:***@host:8080/list.txt"},
		{"https://provider.example/api/list?key=9f8e7d&format=txt", "https://provider.example/api/list?key=***&format=txt"},
		{"https://provider.example/rotate?apiKey=abc&id=7", "https://provider.example/rotate?apiKey=***&id=7"},
		{"https://provider.example/r?access_token=abc#top", "https://provider.example/r?access_token=***#top"},

		// Nothing to hide, and nothing to garble.
		{"socks5://10.0.0.1:1080", "socks5://10.0.0.1:1080"},
		{"10.0.0.1:1080", "10.0.0.1:1080"},
		{"user@10.0.0.1:1080", "user@10.0.0.1:1080"},
		{"https://provider.example/list.txt", "https://provider.example/list.txt"},
		{`C:\proxies\list.txt`, `C:\proxies\list.txt`},
		{"/etc/wbmon/proxies.txt", "/etc/wbmon/proxies.txt"},
		{"", ""},
		// An "@" past the authority is not a credential, and treating it as
		// one would garble an address that is fine.
		{"https://provider.example/list.txt?tag=a@b", "https://provider.example/list.txt?tag=a@b"},
	} {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestRedact_AParseFailureDoesNotQuoteThePassword(t *testing.T) {
	// A line Parse cannot read is reported, and the report is read by a
	// person. A bad port is the ordinary way a line with a good password in it
	// fails to parse.
	_, err := parseLine("socks5://user:secret@10.0.0.1:notaport", "socks5")
	if err == nil {
		t.Fatal("строка с неверным портом разобрана")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("ошибка разбора цитирует пароль: %v", err)
	}

	_, err = parseLine("10.0.0.1:1080:user:secret:extra", "socks5")
	if err == nil {
		t.Fatal("строка из пяти полей разобрана")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("ошибка разбора цитирует пароль: %v", err)
	}
}
