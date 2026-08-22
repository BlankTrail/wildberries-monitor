// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthURL_AsksForWhatARefreshTokenNeeds(t *testing.T) {
	// Google issues a long-lived token only with both of these, and only the
	// first time somebody authorises. Without them a user who cleared the
	// stored token is sent back with an access token that dies in an hour and
	// no way to renew it — an export that works this afternoon and not
	// tomorrow.
	c := Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8760/google/callback"}
	u, err := url.Parse(c.AuthURL("nonce"))
	if err != nil {
		t.Fatalf("AuthURL: %v", err)
	}
	q := u.Query()
	for _, c := range []struct{ key, want string }{
		{"access_type", "offline"},
		{"prompt", "consent"},
		{"response_type", "code"},
		{"state", "nonce"},
		{"scope", ScopeSheets},
		{"redirect_uri", "http://127.0.0.1:8760/google/callback"},
	} {
		if got := q.Get(c.key); got != c.want {
			t.Errorf("%s = %q, ожидалось %q", c.key, got, c.want)
		}
	}
}

func TestState_IsNotTheSameTwice(t *testing.T) {
	// It is the whole of the anti-forgery check. A predictable one is a
	// callback somebody else's page can make on the user's behalf.
	seen := map[string]bool{}
	for range 50 {
		s, err := State()
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s == "" {
			t.Fatal("пустое состояние")
		}
		if seen[s] {
			t.Fatalf("состояние повторилось: %q", s)
		}
		seen[s] = true
	}
}

func TestToken_Valid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, c := range []struct {
		name string
		tok  Token
		want bool
	}{
		{"живой", Token{Access: "a", Expiry: now.Add(time.Hour).Unix()}, true},
		{"истёк", Token{Access: "a", Expiry: now.Add(-time.Second).Unix()}, false},
		{"истекает через секунду", Token{Access: "a", Expiry: now.Add(time.Second).Unix()}, false},
		{"без срока", Token{Access: "a"}, false},
		{"пустой", Token{Expiry: now.Add(time.Hour).Unix()}, false},
	} {
		if got := c.tok.Valid(now); got != c.want {
			t.Errorf("%s: Valid = %v", c.name, got)
		}
	}
}

func TestRefresh_RefusesWithNothingStored(t *testing.T) {
	// The message is what a person acts on: «подключите заново» rather than a
	// 400 from Google about a parameter they never typed.
	c := Config{ClientID: "id", ClientSecret: "secret"}
	if _, err := c.Refresh(context.Background(), "  "); err == nil {
		t.Error("обновление без сохранённого доступа принято")
	}
}

func TestSpreadsheetID_TakesALinkOrAnID(t *testing.T) {
	// What a person has to hand is the thing in their address bar.
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://docs.google.com/spreadsheets/d/1AbC-dEf_23/edit#gid=0", "1AbC-dEf_23", true},
		{"https://docs.google.com/spreadsheets/d/1AbC-dEf_23", "1AbC-dEf_23", true},
		{"https://docs.google.com/spreadsheets/d/1AbC-dEf_23/edit?usp=sharing", "1AbC-dEf_23", true},
		{"  1AbC-dEf_23  ", "1AbC-dEf_23", true},
		{"", "", false},
		{"https://docs.google.com/spreadsheets/d//edit", "", false},
		{"https://example.com/other/thing", "", false},
		{"две части", "", false},
	} {
		got, ok := SpreadsheetID(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%q → %q, %v; ожидалось %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// sheetsAt points a client at a test server, which is what the API constant
// otherwise prevents.
func sheetsAt(t *testing.T, h http.HandlerFunc) (*Sheets, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Sheets{
		Token: func(context.Context) (string, error) { return "token", nil },
		HTTP:  srv.Client(),
	}, srv
}

func TestSheetsAppend_SendsTheRowsAndTheToken(t *testing.T) {
	var gotAuth, gotBody, gotQuery string
	s, srv := sheetsAt(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.Write([]byte(`{}`))
	})
	if err := s.call(context.Background(), http.MethodPost,
		srv.URL+"/values/Sheet1:append?valueInputOption=USER_ENTERED&insertDataOption=INSERT_ROWS",
		struct {
			Values [][]string `json:"values"`
		}{Values: [][]string{{"a", "b"}}}, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if gotAuth != "Bearer token" {
		t.Errorf("заголовок = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"values"`) || !strings.Contains(gotBody, `"a"`) {
		t.Errorf("тело = %q", gotBody)
	}
	// USER_ENTERED is what makes a price a number rather than a string, and
	// INSERT_ROWS is what keeps an append from overwriting what is below.
	if !strings.Contains(gotQuery, "USER_ENTERED") || !strings.Contains(gotQuery, "INSERT_ROWS") {
		t.Errorf("параметры = %q", gotQuery)
	}
}

func TestSheetsAppend_SendsNothingForNoRows(t *testing.T) {
	// A batch of nothing is a round trip that changes nothing, and Close calls
	// this on every export that ended on a batch boundary.
	calls := 0
	s, _ := sheetsAt(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(`{}`))
	})
	if err := s.Append(context.Background(), "id", "Sheet1", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if calls != 0 {
		t.Errorf("сделано %d запросов ни о чём", calls)
	}
}

func TestSheets_ReportsGooglesOwnWords(t *testing.T) {
	// «The caller does not have permission» and «Requested entity was not
	// found» are the same 4xx to a status line and two completely different
	// things to do about it.
	s, srv := sheetsAt(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`))
	})
	err := s.call(context.Background(), http.MethodGet, srv.URL, nil, nil)
	if err == nil {
		t.Fatal("отказ принят за успех")
	}
	if !strings.Contains(err.Error(), "does not have permission") {
		t.Errorf("ошибка = %v — слова Google потерялись", err)
	}
	// Their message, not their JSON. A body dumped whole reads as a stack
	// trace to somebody who wanted to know which setting to change.
	if strings.ContainsAny(err.Error(), "{}") {
		t.Errorf("ошибка = %v — это тело ответа, а не сообщение", err)
	}
}

func TestSheets_RefusesWithoutAToken(t *testing.T) {
	// Reached with no token, the call would go out unauthenticated and come
	// back 401 — an error about the request rather than about the setting
	// nobody filled in.
	s := &Sheets{}
	if err := s.Append(context.Background(), "id", "Sheet1", [][]string{{"a"}}); err == nil {
		t.Error("запрос без токена отправлен")
	}
}

func TestRefresh_KeepsTheLongLivedTokenGoogleDidNotResend(t *testing.T) {
	// Google reissues the long-lived token when it feels like it, and most
	// refreshes come back without one. Storing the empty value would throw
	// away the permission the user gave, and the next export would ask them to
	// authorise again for no reason they could see.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer srv.Close()

	c := Config{ClientID: "id", ClientSecret: "secret", endpoint: srv.URL}
	got, err := c.Refresh(context.Background(), "long-lived")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Access != "new-access" {
		t.Errorf("новый токен = %q", got.Access)
	}
	if got.Refresh != "long-lived" {
		t.Errorf("долгий токен = %q — разрешение пользователя потеряно", got.Refresh)
	}
	if !got.Valid(time.Now()) {
		t.Error("свежий токен считается истёкшим")
	}
}

func TestRefresh_TakesTheNewLongLivedTokenWhenThereIsOne(t *testing.T) {
	// And when Google does send one, it is the one to keep: the old may have
	// been revoked in the same breath.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"access_token":"a","refresh_token":"newer","expires_in":3600}`))
	}))
	defer srv.Close()

	c := Config{ClientID: "id", ClientSecret: "secret", endpoint: srv.URL}
	got, err := c.Refresh(context.Background(), "older")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Refresh != "newer" {
		t.Errorf("долгий токен = %q", got.Refresh)
	}
}

func TestRefresh_DoesNotAskGoogleWithNothingToAskWith(t *testing.T) {
	// A round trip that can only be refused, and an error about a parameter
	// the person never typed instead of «подключите таблицу заново».
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
	}))
	defer srv.Close()

	c := Config{ClientID: "id", ClientSecret: "secret", endpoint: srv.URL}
	if _, err := c.Refresh(context.Background(), "   "); err == nil {
		t.Error("обновление без доступа принято")
	}
	if calls != 0 {
		t.Errorf("сделано %d запросов без сохранённого доступа", calls)
	}
}

func TestExchange_ReportsGooglesOwnRefusal(t *testing.T) {
	// «redirect_uri_mismatch» tells somebody exactly which line of their Cloud
	// console is wrong; «статус 400» tells them nothing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"redirect_uri_mismatch","error_description":"Bad Request"}`))
	}))
	defer srv.Close()

	c := Config{ClientID: "id", ClientSecret: "secret", endpoint: srv.URL}
	_, err := c.Exchange(context.Background(), "code")
	if err == nil {
		t.Fatal("отказ принят за успех")
	}
	if !strings.Contains(err.Error(), "redirect_uri_mismatch") {
		t.Errorf("ошибка = %v", err)
	}
}

func TestToken_RefusesWithoutAClient(t *testing.T) {
	// The message names the two settings that are empty rather than letting
	// Google answer «invalid_client» about them.
	c := Config{endpoint: "http://127.0.0.1:1"}
	if _, err := c.Exchange(context.Background(), "code"); err == nil {
		t.Error("обмен без клиента принят")
	}
}
