// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"html"
	"net/http"
	"strconv"
	"strings"
)

// Products as people recognise them: a picture, the name, and the number
// underneath.
//
// A row that reads «152540730» is a row somebody has to open to know what it
// is; one with the shoe on it is read at a glance. Every table that lists
// products draws its product the same way through productCell, and every
// name-with-an-id pair — a brand, a seller — through labelled.

// productImage answers a thumbnail press with the photograph's address on the
// site's CDN. A redirect rather than the bytes: the browser fetches the
// picture from the CDN the way the site's own pages do, and this program
// spends no proxy port on pictures.
func (s *Server) productImage(w http.ResponseWriter, r *http.Request) {
	nm, err := strconv.ParseInt(r.PathValue("nm"), 10, 64)
	if err != nil || nm <= 0 || s.ProductImage == nil {
		http.NotFound(w, r)
		return
	}
	url, err := s.ProductImage(r.Context(), nm)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A day: the address is arithmetic on a map that changes in months, and
	// a table scrolled twice should not ask twice.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.Redirect(w, r, url, http.StatusFound)
}

// thumb is a product's picture, loaded only when it scrolls into view. One
// that fails is turned into a blank tile by app.js.
func thumb(nm int64) string {
	return `<img class="bt-thumb" src="/img/` + strconv.FormatInt(nm, 10) +
		`" loading="lazy" decoding="async" alt="" referrerpolicy="no-referrer" width="40" height="53">`
}

// productCell is one product as a table cell: the picture, the name over two
// lines at most, and the article underneath — as a link where href is given,
// opening a new tab when it leads off this panel.
func productCell(nm int64, name, href string) string {
	var b strings.Builder
	b.WriteString(`<td class="bt-product"><div class="bt-product__box">` + thumb(nm) + `<span class="bt-product__text">`)
	if strings.TrimSpace(name) != "" {
		b.WriteString(`<span class="bt-product__name" title="` + html.EscapeString(name) + `">` +
			html.EscapeString(name) + `</span>`)
	}
	id := "ID " + strconv.FormatInt(nm, 10)
	switch {
	case strings.HasPrefix(href, "http"):
		id = `<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener">` + id + `</a>`
	case href != "":
		id = `<a href="` + html.EscapeString(href) + `">` + id + `</a>`
	}
	b.WriteString(`<span class="bt-sub">` + id + `</span></span></div></td>`)
	return b.String()
}

// labelled is a name with its number underneath: a brand and its id, a seller
// and theirs. Either half may be missing.
func labelled(name string, id *int64) string {
	var b strings.Builder
	if strings.TrimSpace(name) != "" {
		b.WriteString(`<span class="bt-labelled__name">` + html.EscapeString(name) + `</span>`)
	} else {
		b.WriteString(`<span class="bt-labelled__name">—</span>`)
	}
	// Zero is the site's «no brand», not a brand numbered nought: «— ID 0»
	// stood under every unbranded product on the sales screen (09.10.2026).
	if id != nil && *id != 0 {
		b.WriteString(`<span class="bt-sub">ID ` + strconv.FormatInt(*id, 10) + `</span>`)
	}
	return b.String()
}

// priceStack is the price as the site shows it: what is paid, and under it,
// smaller, what it was before the discount.
func priceStack(sale, base string) string {
	out := `<span class="bt-price">` + html.EscapeString(sale) + `</span>`
	if base != "" && base != sale {
		out += `<span class="bt-sub bt-sub--was">` + html.EscapeString(base) + `</span>`
	}
	return out
}
