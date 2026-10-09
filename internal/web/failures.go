// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BlankTrail/wildberries-monitor/internal/job"
)

// This file is how a lost item reads on «Подробнее».
//
// The list used to print the item's storage key and the error as the program
// wrote it: «page|футболка оверсайз|-1257786|1|7» beside four lines of English
// carrying the whole search address three times. Seen on a real run of a cheap
// proxy list (09.10.2026): fourteen such rows, and the one fact a person needed —
// the proxies did not get through, the site was never asked — was in the last
// six words. The original stays one click away, under the wording.

// itemTitle is what an item is, in the words the live log uses for it.
func itemTitle(names map[int64]string, key string) string {
	k, err := job.ParseKey(key)
	if err != nil {
		return key
	}
	where := ""
	if k.Dest != "" {
		where = ", " + regionLabel(names, k.Dest)
	}
	switch k.Kind {
	case job.ItemPage:
		return fmt.Sprintf("выдача «%s», страница %d%s", k.Phrase, k.Page, where)
	case job.ItemListing:
		return fmt.Sprintf("витрина продавца %d, страница %d%s", k.ID, k.Page, where)
	case job.ItemCatalog:
		return fmt.Sprintf("категория %d, страница %d%s", k.ID, k.Page, where)
	case job.ItemPromo:
		return fmt.Sprintf("акция %d, страница %d%s", k.ID, k.Page, where)
	case job.ItemMain:
		return fmt.Sprintf("главная страница, страница %d%s", k.Page, where)
	case job.ItemSeller:
		return fmt.Sprintf("продавец %d", k.ID)
	case job.ItemBrand:
		return fmt.Sprintf("бренд %d", k.ID)
	case job.ItemProduct:
		return fmt.Sprintf("товар %d%s", k.NmID, where)
	case job.ItemDetails:
		return fmt.Sprintf("карточки: %d артикулов%s", len(k.NmIDs), where)
	case job.ItemShelf:
		return fmt.Sprintf("полка под товаром %d", k.NmID)
	case job.ItemAds:
		return fmt.Sprintf("реклама по фразе «%s»%s", k.Phrase, where)
	case job.ItemProfile:
		return fmt.Sprintf("разбор ссылки на товар %d", k.NmID)
	}
	return key
}

var (
	givingUp = regexp.MustCompile(`giving up after (\d+) attempt\(s\) over (\d+) port\(s\), (\d+) egress change\(s\), (\d+) of them lost before a response`)
	portSaid = regexp.MustCompile(`request never reached the origin \((\d+) ([a-z_]+)\)`)
	statusWB = regexp.MustCompile(`status (\d{3})\b`)
)

// portReasons is what a port's own refusal means, by the tag it gives.
var portReasons = map[string]string{
	"upstream_unreachable":    "прокси не отвечает",
	"mitm_upstream":           "прокси сам вскрывает TLS — BlankTrail такие выходы не пропускает",
	"solver_failed":           "проверку Wildberries не удалось пройти с этого выхода",
	"solver_timeout":          "проверка Wildberries не успела пройти",
	"solver_capacity":         "решатель проверок был занят",
	"chain_unreachable":       "не отвечает первый переход цепочки",
	"origin_handshake_failed": "не сложилось TLS-соединение с Wildberries",
	"port_conn_limit":         "порт был перегружен",
}

// failureReason says in Russian why an item was given up on. What it cannot
// read it leaves out rather than guesses; reasonRaw keeps the original.
// channelDown is the pool's word for a channel whose every exit failed.
var channelDown = regexp.MustCompile(`channel\(s\) (.+?): (\d+) exits in a row`)

func failureReason(raw string) string {
	if raw == "" {
		return "причина не записана"
	}
	if m := channelDown.FindStringSubmatch(raw); m != nil {
		return fmt.Sprintf("канал «%s» не работает: %s выходов подряд не пропустили запрос, и ни один не ответил. "+
			"Обычно это значит, что провайдер не принимает логин, кончился трафик или сервис недоступен — "+
			"проверьте прокси в личном кабинете провайдера.", m[1], m[2])
	}
	var parts []string
	if m := givingUp.FindStringSubmatch(raw); m != nil {
		s := fmt.Sprintf("%s попыток через %s портов", m[1], m[2])
		if m[3] != "0" {
			s += ", выход менялся " + m[3] + " раз"
		}
		if m[4] == m[1] {
			s += "; ни одна не получила ответа"
		} else if m[4] != "0" {
			s += "; без ответа — " + m[4]
		}
		parts = append(parts, s)
	}
	if m := portSaid.FindAllStringSubmatch(raw, -1); m != nil {
		last := m[len(m)-1]
		what, ok := portReasons[last[2]]
		if !ok {
			what = "порт BlankTrail отказал"
		}
		parts = append(parts, fmt.Sprintf("запрос не дошёл до Wildberries: %s (%s %s)", what, last[1], last[2]))
	} else if m := statusWB.FindAllStringSubmatch(raw, -1); m != nil {
		parts = append(parts, "Wildberries ответил кодом "+m[len(m)-1][1])
	}
	if len(parts) == 0 {
		return raw
	}
	return strings.Join(parts, ". ") + "."
}
