// SPDX-License-Identifier: AGPL-3.0-or-later

// Package phrase turns a card into the searches somebody might be found in.
//
// Spec section 4.7 explains why this exists rather than being read from
// somewhere: Wildberries publishes no list of the phrases a seller ranks for.
// It lives in their own cabinet, and the Seller API is outside this product.
// So the phrases are derived from what the card says, and every one of them is
// a guess until a position check either promotes it to «рабочая» or puts it
// aside.
//
// Nothing here touches the network. Candidates are made from what has already
// been collected, which is what makes this half of onboarding free — and the
// checking, which is the expensive half, is a job the user prices first.
package phrase

import (
	"strings"
	"unicode"
)

// maxWords is the longest candidate this makes.
//
// Four words, because a search longer than that is a sentence rather than a
// query: it either returns the product alone — which measures nothing — or
// nothing at all, and either way it costs a check to find out.
const maxWords = 4

// stopWords are the words that carry no search intent on their own.
//
// Short and deliberately dull: prepositions, conjunctions and the handful of
// filler words Russian card names are written with. A longer list would start
// dropping words that matter — «для» in «для дома» is filler, «дом» is not,
// and the line between them is exactly here.
var stopWords = map[string]bool{
	"и": true, "в": true, "на": true, "с": true, "по": true, "из": true,
	"для": true, "от": true, "до": true, "за": true, "к": true, "о": true,
	"или": true, "а": true, "но": true, "не": true, "как": true, "что": true,
	"шт": true, "см": true, "мл": true, "гр": true, "кг": true,
}

// Source is what a card offers to make candidates out of.
//
// A struct rather than the wb types themselves, so this package does not
// depend on the site's model: the fields it needs are four strings and a list
// of characteristic values, and a caller that has them from anywhere can use
// this.
type Source struct {
	Name         string   // the product name as the seller wrote it
	Brand        string   // the brand, for «бренд + предмет» pairs
	Subject      string   // what the thing is, from the card's own category
	SubjectRoot  string   // the wider category above it
	Options      []string // characteristic values: colour, material, purpose
	MaxPerSource int      // how many candidates one card may produce, 0 for the default
}

// defaultMax is how many candidates one card produces when nobody says.
//
// Section 4.7 calls the checking «самая дорогая часть онбординга» and asks for
// a limit on the screen. This is the number that limit starts at: twenty
// phrases per product is a check somebody can afford to run and read.
const defaultMax = 20

// Candidates are the searches this card might be found in, best guesses first.
//
// «Best» here means «most specific»: the whole name is the phrase the seller
// themselves chose, the pairs under it are what a buyer types, and the single
// words are last because a one-word search returns a category rather than a
// product. Nothing is invented — every candidate is words that were already
// on the card, in the order the card had them.
func Candidates(src Source) []string {
	limit := src.MaxPerSource
	if limit <= 0 {
		limit = defaultMax
	}

	seen := map[string]bool{}
	var out []string
	add := func(phrase string) {
		phrase = strings.TrimSpace(phrase)
		if phrase == "" || seen[phrase] || len(out) >= limit {
			return
		}
		seen[phrase] = true
		out = append(out, phrase)
	}

	name := tokens(src.Name)
	subject := words(src.Subject)
	brand := words(src.Brand)

	// The name itself, when it is short enough to be a search rather than a
	// description.
	if run := trimRun(name); contentWords(run) > 0 && contentWords(run) <= maxWords {
		add(strings.Join(run, " "))
	}

	// The beginnings of the name, longest first.
	//
	// Beginnings and not every run inside it, which is the second half of what
	// made these unreadable. A Russian card name starts with the thing itself
	// and adds to it — «топ на бретелях в спортивном стиле» — so a prefix is a
	// phrase and a slice out of the middle is a fragment in whatever case it
	// happened to be written in. «бретелях в спортивном» is a run of that name
	// and nobody has ever typed it.
	//
	// What this does not fix, said plainly: a prefix can still end on an
	// adjective whose noun is the next word — «топ на бретелях в спортивном».
	// Telling that from «топ на бретелях» needs to know that «спортивном» is an
	// adjective, which needs Russian morphology, which is a dictionary this
	// program does not carry. It is a truncation of the seller's own name
	// rather than an invention, and it costs one request to find out.
	for size := min(maxWords, contentWords(name)); size >= 2; size-- {
		if run := prefix(name, size); contentWords(run) == size {
			add(strings.Join(run, " "))
		}
	}

	// What the thing is: one word, and the broadest search that is still about
	// this product. After the name, because a one-word search returns a
	// category rather than a product.
	if len(subject) > 0 {
		add(strings.Join(subject, " "))
	}

	// The brand with the subject, which is how somebody looks for this exact
	// maker's version of the thing.
	if len(brand) > 0 && len(subject) > 0 {
		add(strings.Join(brand, " ") + " " + strings.Join(subject, " "))
	}

	// The wider category last: it is the broadest search there is, and the
	// one a product is least likely to be found in — but it is also the one
	// worth knowing about.
	if root := words(src.SubjectRoot); len(root) > 0 {
		add(strings.Join(root, " "))
	}

	return out
}

// tokens is a card's name as the words of a search, in the order it wrote
// them and with nothing taken out of the middle.
//
// The difference from words() is the whole of what makes a candidate readable.
// words() drops «на» and «в», which is right for a subject or a brand — those
// are one or two words and the little ones are noise. Inside a name it is the
// opposite: «Топ на бретелях в спортивном стиле» became «топ бретелях
// спортивном стиле», and every run cut out of it read like a telegram.
//
// Case folded and punctuation dropped, as before, so that «Платье, летнее!» is
// the same phrase as «платье летнее».
func tokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var out []string
	for _, f := range fields {
		if stopWords[f] || isContent(f) {
			out = append(out, f)
		}
	}
	return out
}

// isContent reports whether a token carries meaning of its own.
func isContent(f string) bool {
	if stopWords[f] {
		return false
	}
	// Two-letter words are kept only when they are numbers: «40» is a size,
	// «на» is not a search.
	return len([]rune(f)) >= 3 || isDigits(f)
}

// trimRun drops the little words from the ends of a run.
//
// «в спортивном стиле» is a phrase; «в спортивном» and «стиле в» are the
// halves of one, and a search box is not where anybody types them.
func trimRun(run []string) []string {
	for len(run) > 0 && !isContent(run[0]) {
		run = run[1:]
	}
	for len(run) > 0 && !isContent(run[len(run)-1]) {
		run = run[:len(run)-1]
	}
	return run
}

// prefix is the start of a name up to n words that mean something, with the
// little words in between kept and none left hanging off the end.
func prefix(name []string, n int) []string {
	seen := 0
	for i, w := range name {
		if isContent(w) {
			seen++
		}
		if seen == n {
			return trimRun(name[:i+1])
		}
	}
	return trimRun(name)
}

// contentWords counts the words in a run that mean anything.
//
// A run has to have two of them to be a phrase rather than a word with a
// preposition stuck to it.
func contentWords(run []string) int {
	n := 0
	for _, w := range run {
		if isContent(w) {
			n++
		}
	}
	return n
}

// words normalises a piece of a card into the words a search is made of.
//
// Case folded, punctuation dropped, digits kept: «45 размер» is a real search
// and «Платье, летнее!» is the same phrase as «платье летнее». Words shorter
// than three letters go, along with the stop words, because a search is not
// improved by «и».
func words(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var out []string
	for _, f := range fields {
		if stopWords[f] {
			continue
		}
		// Two-letter words are kept only when they are numbers: «40» is a
		// size, «на» is not a search.
		if len([]rune(f)) < 3 && !isDigits(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// Clean turns one of the site's suggestions into a phrase worth a request.
//
// The suggestions come back as the search box would put them in — the spacing
// is the box's, not a person's — and they are otherwise exactly what people
// type, so this normalises and refuses rather than rewrites. A phrase edited
// here would no longer be the phrase the site suggested, which is the whole
// reason to ask it.
func Clean(text string) string {
	out := strings.Join(strings.Fields(text), " ")
	if out == "" {
		return ""
	}
	// Longer than a search is a sentence, and the same ceiling the candidates
	// are made under: it either returns the one product or nothing, and either
	// way a request finds that out.
	if len(strings.Fields(out)) > maxWords+2 {
		return ""
	}
	// A phrase this program cannot key an item on is a phrase a resumed run
	// would match against the wrong work — see internal/job's keySep.
	if strings.ContainsAny(out, "|") {
		return ""
	}
	return out
}
