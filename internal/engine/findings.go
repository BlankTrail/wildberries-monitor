// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BlankTrail/wildberries-monitor/blanktrail"
	"github.com/BlankTrail/wildberries-monitor/wb"
)

// This file says the preflight's findings in the panel's language.
//
// The SDK writes them in English and is right to: it is a library for any
// target, and a library that picked a reader's language would pick wrong for
// most of its readers. The panel is in Russian, and a finding pasted into a
// Russian sentence — «прокси не готов: Challenge Breaker is not included in
// this tariff — Upgrade to a plan…» — is what a run's failure used to read
// like. So the words are chosen here, keyed on the finding's stable ID, the
// way Google_GO keys its own.
//
// The numbers and names a finding is about are read from the report and the
// input rather than out of the SDK's sentence: which domains are missing, how
// many solver processes the licence allows, how many ports the run needs.

// Said is one finding as the panel shows it.
type Said struct {
	Severity blanktrail.Severity
	Title    string
	// Detail is what is wrong. For a finding whose cause is an error from the
	// service, the error's own text is kept here: it is the part somebody
	// pastes into a support chat, and translating it would lose what support
	// needs to read.
	Detail string
	Action string
}

// say is one finding in Russian. An ID this build does not know keeps the
// SDK's own words rather than being dropped: a finding nobody can read is
// better than one nobody sees.
func say(f blanktrail.Finding, rep blanktrail.Report, in blanktrail.PreflightInput) Said {
	s := Said{Severity: f.Severity, Detail: f.Detail}
	lic := rep.License
	switch f.ID {
	case "unauthorized":
		s.Title = "BlankTrail не принял ключ API"
		s.Action = "Скопируйте действующий ключ в BlankTrail → Настройки → Ключ API и вставьте его в настройки подключения."
	case "unreachable":
		s.Title = "BlankTrail не отвечает"
		s.Action = "Запустите BlankTrail Proxy и проверьте, что его управляющий API слушает адрес из настроек " +
			"(по умолчанию 127.0.0.1:8891)."
	case "license_unreadable":
		s.Title = "Не удалось прочитать состояние лицензии BlankTrail"
		s.Action = "Откройте панель BlankTrail и проверьте, что лицензия активирована."
	case "license_inactive":
		s.Title = "Лицензия BlankTrail не активирована"
		s.Detail = "BlankTrail сообщает о неактивной лицензии и не будет открывать порты."
		s.Action = "Активируйте лицензию в панели BlankTrail и повторите проверку."
	case "challenge_breaker":
		s.Title = "В тариф не входит Challenge Breaker"
		s.Detail = "Wildberries закрыт JavaScript-проверкой: без решателя вместо данных приходит страница проверки."
		s.Action = "Перейдите на тариф с Challenge Breaker в кабинете BlankTrail."
	case "solver_capacity":
		s.Title = "Challenge Breaker входит в тариф, но выключен"
		s.Detail = fmt.Sprintf("Лицензия разрешает до %d процессов решателя, но не включено ни одного — "+
			"ни одна проверка не будет решена.", lic.JsSolverMaxProcs)
		s.Action = fmt.Sprintf("Задайте число процессов Challenge Breaker в панели BlankTrail (потолок лицензии: %d).",
			lic.JsSolverMaxProcs)
	case "pool":
		s.Title = "В тариф не входит пул портов"
		s.Detail = fmt.Sprintf("Нужно портов: %d, а лицензия разрешает один.", in.Ports)
		s.Action = "Перейдите на тариф с пулом портов: каждое задание открывает по два порта на поток."
	case "domains":
		s.Title = "Тариф не покрывает домены, без которых сбор не работает"
		s.Detail = fmt.Sprintf("Лицензия %s ограничена списком доменов, в нём нет: %s.",
			licenceName(lic), strings.Join(lic.MissingDomains(in.Domains), ", "))
		s.Action = "Попросите добавить эти домены в лицензию — удобнее шаблоном, например *.wbbasket.ru, — " +
			"или перейдите на тариф без ограничения доменов."
	case "optional_domains":
		s.Title = "Тариф не покрывает домены некоторых данных"
		s.Detail = fmt.Sprintf("Не покрыты: %s. Отзывы, вопросы, акции или профиль продавца с них собираться не будут, "+
			"остальное соберётся.", strings.Join(lic.MissingDomains(in.OptionalDomains), ", "))
		s.Action = "Добавьте их в лицензию, если эти данные нужны."
	case "gateways_unlisted":
		s.Title = "Не удалось получить список шлюзов BlankTrail"
		s.Action = "Шлюзы не будут предложены как выходы. Списки прокси и прямое соединение работают."
	case "gateways":
		s.Title = "Шлюзы BlankTrail недоступны"
		s.Action = "Установите в BlankTrail модуль шлюзов, если хотите выходить через VPN-профили."
	case "ca":
		s.Title = "Не удалось получить сертификат BlankTrail"
		s.Action = "Без него ни один HTTPS-запрос через порт не пройдёт проверку. " +
			"Проверьте, что BlankTrail создал свой сертификат."
	default:
		s.Title, s.Detail, s.Action = f.Title, f.Detail, f.Action
	}
	return s
}

// sayAll is a whole report in Russian, in the order the SDK produced it.
func sayAll(rep blanktrail.Report, in blanktrail.PreflightInput) []Said {
	out := make([]Said, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		out = append(out, say(f, rep, in))
	}
	return out
}

// licenceName is how a licence is named in a sentence: its plan and label,
// in guillemets, or nothing a person could mistake for a name.
func licenceName(lic blanktrail.LicenseStatus) string {
	name := strings.TrimSpace(lic.Plan + " " + lic.Label)
	if name == "" {
		return "этого тарифа"
	}
	return "«" + name + "»"
}

// preflightFor is the preflight a pool of ports ports is checked with: every
// host a collection talks to, required and optional — see
// wb.Endpoints.PreflightHosts — and the number of ports it will open.
func preflightFor(eps wb.Endpoints, ports int) blanktrail.PreflightInput {
	required, optional := eps.PreflightHosts()
	return blanktrail.PreflightInput{Domains: required, OptionalDomains: optional, Ports: ports}
}

// logFindings puts a report in the log, one line a finding, in Russian.
func (e *Engine) logFindings(rep blanktrail.Report, in blanktrail.PreflightInput) {
	for _, s := range sayAll(rep, in) {
		e.logf("прокси: [%s] %s — %s", s.Severity, s.Title, s.Action)
	}
}

// firstBlocking is the finding to put in an error.
//
// One rather than all of them: every finding has already gone to the log with
// its own remedy, and an error message carrying four paragraphs is one nobody
// reads to the end. The first blocking one is the one to fix first.
func firstBlocking(rep blanktrail.Report, in blanktrail.PreflightInput) string {
	for _, f := range rep.Blocking() {
		s := say(f, rep, in)
		return s.Title + " — " + s.Action
	}
	return "причина не названа"
}

// checkTimeout bounds the settings screen's check. Somebody pressed a button
// and is watching it; a service that has stopped answering must cost them
// seconds, not the browser's patience.
const checkTimeout = 15 * time.Second

// CheckConnection is the settings screen's «Проверить соединение»: the whole preflight
// a run makes, against what is configured now, said in Russian.
//
// The whole of it, not a health call. A health call answers «is something
// listening», and the screen said «соединение установлено» over an inactive
// licence or a switched-off solver — the two things that actually stop a run.
// Checked for the smallest run there is, one thread, so a licence without the
// port pool is reported here rather than at the first start.
//
// A report with nothing blocking leads with what was found: the plan and the
// solver, so «всё в порядке» says what it is in order.
func (e *Engine) CheckConnection(ctx context.Context) ([]Said, error) {
	client, err := e.control(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	in := preflightFor(e.Endpoints, portsPerThread)
	rep := blanktrail.Preflight(ctx, client, in)
	out := sayAll(rep, in)
	if rep.OK() {
		lic := rep.License
		detail := fmt.Sprintf("Challenge Breaker: процессов %d из %d.", lic.JsSolverProcs, lic.JsSolverMaxProcs)
		if name := strings.TrimSpace(lic.Plan + " " + lic.Label); name != "" {
			detail = "Тариф «" + name + "». " + detail
		}
		summary := Said{
			Severity: blanktrail.SeverityOK,
			Title:    "Соединение установлено, лицензия активна",
			Detail:   detail,
		}
		out = append([]Said{summary}, out...)
	}
	return out, nil
}
