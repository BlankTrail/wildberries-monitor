// SPDX-License-Identifier: AGPL-3.0-or-later

// Package google is the little of Google's API this product needs: the OAuth
// dance that gets a token, and the three Sheets calls that use it.
//
// Written against the HTTP endpoints rather than through Google's own Go
// libraries, and that is a decision about what this program is. It has four
// direct dependencies; google.golang.org/api brings a hundred, for a feature
// that amounts to one POST with a JSON body. Everything here is standard
// library — an RSA-free flow, a JSON exchange, and a token that is refreshed
// when it expires.
//
// Nothing in this package ships a credential, and nothing in this repository
// does. Google's own documentation says an installed application's client
// secret is not confidential, and plenty of open-source desktop apps ship one;
// this one does not, because a public repository that carries somebody's OAuth
// client is a repository that carries somebody's OAuth client. The user makes
// their own in Google Cloud and pastes it into the settings, the same way they
// paste a Telegram token.
package google

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The endpoints of the flow. Constants rather than settings: unlike
// Wildberries, which moves its addresses without notice, these are a published
// contract with a version in the path.
const (
	authEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint = "https://oauth2.googleapis.com/token"

	// ScopeSheets is the narrowest scope that can write to a spreadsheet.
	//
	// Not drive.file, which would also let this program create and see files:
	// it writes into a sheet the user made and shared, and asking for the power
	// to make more of them is asking for something it does not use.
	ScopeSheets = "https://www.googleapis.com/auth/spreadsheets"
)

// exchangeTimeout bounds a token call. Two small JSON round trips to Google,
// made while somebody is looking at a browser tab.
const exchangeTimeout = 30 * time.Second

// refreshSkew is how long before expiry a token is replaced.
//
// A minute. An export that starts with fifty seconds left on its token would
// otherwise fail halfway through a batch, and the failure would look like the
// spreadsheet refusing the data.
const refreshSkew = time.Minute

// Config is the OAuth client the user made in their own Google Cloud project.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL is where Google sends the browser back to. For a program
	// that serves a panel on this machine it is that panel's own address — the
	// loopback redirect Google documents for installed applications.
	RedirectURL string

	// endpoint is where the token is asked for, and it is empty everywhere in
	// this program: Google's own address is the only one that answers. It
	// exists so that this package's tests can exercise the exchange and the
	// refresh — which is where the one rule that matters lives, that a refresh
	// response without a long-lived token must not erase the one it was made
	// from. Unexported, so nothing outside can point the flow elsewhere.
	endpoint string
}

// Token is what the flow produces.
type Token struct {
	Access  string
	Refresh string
	// Expiry is when Access stops working, as Unix seconds. Zero means «не
	// знаем», which is treated as expired: asking for a fresh one costs a
	// round trip, using a dead one costs the export.
	Expiry int64
}

// Valid reports whether the access token can still be used.
func (t Token) Valid(now time.Time) bool {
	return t.Access != "" && t.Expiry > now.Add(refreshSkew).Unix()
}

// State is a fresh anti-forgery value for one authorisation.
//
// Google hands it back on the redirect, and a callback that arrives with a
// state nobody issued is a request somebody else's page made on the user's
// behalf. Random rather than derived from anything: it has to be unguessable.
func State() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("google: state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthURL is where the browser is sent to ask the user for permission.
func (c Config) AuthURL(state string) string {
	q := url.Values{
		"client_id":     {c.ClientID},
		"redirect_uri":  {c.RedirectURL},
		"response_type": {"code"},
		"scope":         {ScopeSheets},
		// A refresh token is only issued with both of these, and only the
		// first time — which is why «consent» is forced rather than left to
		// Google: a user who authorised this app last month and then cleared
		// the stored token would otherwise be sent back with an access token
		// that dies in an hour and no way to renew it.
		"access_type": {"offline"},
		"prompt":      {"consent"},
		"state":       {state},
	}
	return authEndpoint + "?" + q.Encode()
}

// Exchange turns the code Google put on the redirect into a token.
func (c Config) Exchange(ctx context.Context, code string) (Token, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURL},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	})
}

// Refresh renews an access token from the long-lived one.
//
// The refresh token that comes back is often empty: Google reissues it only
// when it decides to, and a caller that stored the empty one would lose the
// permission the user gave. Keeping the old one is this function's job because
// it is the one place that knows the response's shape.
func (c Config) Refresh(ctx context.Context, refresh string) (Token, error) {
	if strings.TrimSpace(refresh) == "" {
		return Token{}, errors.New("google: refresh: нет сохранённого доступа — подключите таблицу заново")
	}
	t, err := c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	})
	if err != nil {
		return Token{}, err
	}
	if t.Refresh == "" {
		t.Refresh = refresh
	}
	return t, nil
}

// token makes one call to the token endpoint.
func (c Config) token(ctx context.Context, form url.Values) (Token, error) {
	if strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" {
		return Token{}, errors.New("google: не заданы идентификатор и секрет клиента OAuth")
	}
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()

	where := c.endpoint
	if where == "" {
		where = tokenEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, where,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("google: token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("google: token: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("google: token: %w", err)
	}

	var raw struct {
		Access      string `json:"access_token"`
		Refresh     string `json:"refresh_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Token{}, fmt.Errorf("google: token: ответ не разобрать: %w", err)
	}
	if raw.Error != "" {
		// Google's own words, because they are specific and this program's
		// paraphrase would not be: «redirect_uri_mismatch» tells somebody
		// exactly which line of their Cloud console is wrong.
		msg := raw.Error
		if raw.Description != "" {
			msg += ": " + raw.Description
		}
		return Token{}, fmt.Errorf("google: %s", msg)
	}
	if res.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("google: token: статус %d", res.StatusCode)
	}
	if raw.Access == "" {
		return Token{}, errors.New("google: token: в ответе нет токена")
	}

	t := Token{Access: raw.Access, Refresh: raw.Refresh}
	if raw.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second).Unix()
	}
	return t, nil
}
