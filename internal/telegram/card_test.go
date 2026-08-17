// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeCards answers as told and records what it was asked about.
type fakeCards struct {
	card  string
	err   error
	asked []int64
}

func (f *fakeCards) Card(_ context.Context, nmID int64) (string, error) {
	f.asked = append(f.asked, nmID)
	return f.card, f.err
}

func TestPoll_CardSendsWhatTheCardsSourceRendered(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/card 123456789")

	cards := &fakeCards{card: "Куртка зимняя — BrandCo\nЦена: 3000.00 RUB\nhttps://example.invalid/1"}
	c := commandsFor(t, api, &fakeJobs{})
	c.Cards = cards

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(cards.asked) != 1 || cards.asked[0] != 123456789 {
		t.Errorf("спросили про %v", cards.asked)
	}
	said := sent(api)
	if len(said) != 1 || said[0] != cards.card {
		t.Errorf("отправлено %q", said)
	}
}

func TestPoll_CardTakesALinkWhereItTakesAnArticle(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/card https://www.wildberries.ru/catalog/123456789/detail.aspx")

	cards := &fakeCards{card: "карточка"}
	c := commandsFor(t, api, &fakeJobs{})
	c.Cards = cards

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(cards.asked) != 1 || cards.asked[0] != 123456789 {
		t.Errorf("из ссылки взяли %v", cards.asked)
	}
}

func TestPoll_CardWithNothingIdentifiableSaysWhatItWants(t *testing.T) {
	for _, text := range []string{"/card", "/card кофемолка"} {
		api := newFakeAPI(t)
		queueUpdates(api, text)

		cards := &fakeCards{card: "карточка"}
		c := commandsFor(t, api, &fakeJobs{})
		c.Cards = cards

		if _, err := c.Poll(t.Context()); err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if len(cards.asked) != 0 {
			t.Errorf("%q всё равно пошло за карточкой", text)
		}
		if said := sent(api); len(said) != 1 || !strings.Contains(said[0], "/card 123456789") {
			t.Errorf("%q ответили %v — без примера", text, said)
		}
	}
}

func TestPoll_CardOfSomethingNeverCollectedIsPassedOnAsItReads(t *testing.T) {
	// Nothing collected yet is the ordinary answer for an article somebody has
	// just heard of. This package only has to not dress it up as a breakage.
	api := newFakeAPI(t)
	queueUpdates(api, "/card 123456789")

	c := commandsFor(t, api, &fakeJobs{})
	c.Cards = &fakeCards{err: errors.New("123456789: этот товар ещё ни разу не собирался")}

	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	said := sent(api)
	if len(said) != 1 || !strings.Contains(said[0], "ни разу не собирался") {
		t.Errorf("ответ %v", said)
	}
	if strings.Contains(said[0], "Не удалось") {
		t.Errorf("обычный ответ подан сбоем: %q", said[0])
	}
}

func TestPoll_SaysSoWhenTheBuildHasNoCards(t *testing.T) {
	api := newFakeAPI(t)
	queueUpdates(api, "/card 123456789")

	c := commandsFor(t, api, &fakeJobs{})
	if _, err := c.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if said := sent(api); len(said) != 1 || !strings.Contains(said[0], "недоступны") {
		t.Errorf("ответ %v", said)
	}
}

func TestHelp_MentionsTheCardCommand(t *testing.T) {
	if !strings.Contains(helpText, "/card") {
		t.Error("в справке нет /card")
	}
}
