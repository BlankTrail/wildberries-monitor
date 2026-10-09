// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/BlankTrail/wildberries-monitor/internal/store"
)

// labels names the products and regions a pass of the rules talks about.
//
// The messages said «товар 34638719 … (регион -1198059)»: two numbers a person
// reading a notification on a phone has to look up to know what moved and
// where (10.10.2026). One pass asks the store once per product and region and
// keeps the answer.
type labels struct {
	ctx   context.Context
	store *store.Store

	mu       sync.Mutex
	products map[int64]string
	regions  map[string]string
}

func (a *App) newLabels(ctx context.Context) *labels {
	return &labels{ctx: ctx, store: a.Store, products: map[int64]string{}, regions: map[string]string{}}
}

// product is a product's name, shortened for a chat line, with its article —
// or the article alone when the store has no name for it.
func (l *labels) product(nm int64) string {
	if l == nil || l.store == nil {
		return fmt.Sprintf("товар %d", nm)
	}
	l.mu.Lock()
	name, ok := l.products[nm]
	l.mu.Unlock()
	if !ok {
		if names, err := l.store.ProductNames(l.ctx, []int64{nm}); err == nil {
			name = names[nm]
		}
		l.mu.Lock()
		l.products[nm] = name
		l.mu.Unlock()
	}
	if name = strings.TrimSpace(name); name == "" {
		return fmt.Sprintf("товар %d", nm)
	}
	return fmt.Sprintf("«%s» (%d)", shorten(name, productNameShown), nm)
}

// productNameShown is how much of a product's name a chat line carries.
const productNameShown = 60

// region is a region code's name: the directory's, Wildberries' own default
// called what it is, or the code when nobody has named it.
func (l *labels) region(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if code == store.DefaultProfileRegion {
		return "Москва (по умолчанию)"
	}
	if l == nil || l.store == nil {
		return "регион " + code
	}
	l.mu.Lock()
	name, ok := l.regions[code]
	l.mu.Unlock()
	if !ok {
		if dest, err := strconv.ParseInt(code, 10, 64); err == nil {
			name, _ = l.store.RegionName(l.ctx, dest)
		}
		l.mu.Lock()
		l.regions[code] = name
		l.mu.Unlock()
	}
	if name == "" {
		return "регион " + code
	}
	return name
}

// shorten cuts a name to n characters at a word, with an ellipsis.
func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := string([]rune(s)[:n])
	if i := strings.LastIndexByte(cut, ' '); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

// plural picks the Russian form for n: один товар, два товара, пять товаров.
func plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// roubles is an amount in kopecks the way a price is read: «2 016 ₽», with
// kopecks only when there are any.
func roubles(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "−", -minor
	}
	whole, kop := minor/100, minor%100
	s := groupThousands(whole)
	if kop != 0 {
		s += fmt.Sprintf(",%02d", kop)
	}
	return sign + s + " ₽"
}

// groupThousands writes 1234567 as «1 234 567», with a narrow no-break space.
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}
