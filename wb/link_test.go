// SPDX-License-Identifier: AGPL-3.0-or-later

package wb

import "testing"

func TestNmID_TakesTheNumberOrTheAddressAndNothingElse(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want int64
		ok   bool
	}{
		{"артикул", "123456789", 123456789, true},
		{"артикул с пробелами", "  123456789\n", 123456789, true},
		{"адрес карточки", "https://www.wildberries.ru/catalog/123456789/detail.aspx", 123456789, true},
		{"адрес с параметрами", "https://wildberries.ru/catalog/123456789/detail.aspx?targetUrl=SG&size=1", 123456789, true},
		{"адрес с якорем", "https://www.wildberries.ru/catalog/123456789/feedbacks#top", 123456789, true},
		{"адрес без схемы", "wildberries.ru/catalog/123456789/detail.aspx", 123456789, true},
		{"адрес другой страны", "https://www.wildberries.by/catalog/123456789/detail.aspx", 123456789, true},
		{"путь в верхнем регистре", "https://WWW.WILDBERRIES.RU/CATALOG/123456789/DETAIL.ASPX", 123456789, true},
		{"адрес и ничего больше", "https://www.wildberries.ru/catalog/123456789/", 123456789, true},

		{"пусто", "", 0, false},
		{"только пробелы", "   ", 0, false},
		{"ноль", "0", 0, false},
		{"отрицательный", "-123456789", 0, false},
		// The reason a bare number in a sentence is refused: this one has two,
		// and picking either is a guess presented as an answer.
		{"число в фразе", "лови 5 штук по 12000", 0, false},
		{"адрес без числа", "https://www.wildberries.ru/catalog/detail.aspx", 0, false},
		{"не тот путь", "https://www.wildberries.ru/seller/123456789", 0, false},
		{"слишком длинное число", "https://www.wildberries.ru/catalog/99999999999999999999999/detail.aspx", 0, false},
		{"артикул и фраза", "123456789 кофемолка", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := NmID(c.text)
			if ok != c.ok || got != c.want {
				t.Errorf("NmID(%q) = %d, %v; ожидалось %d, %v", c.text, got, ok, c.want, c.ok)
			}
		})
	}
}
