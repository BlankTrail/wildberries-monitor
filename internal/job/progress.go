// SPDX-License-Identifier: AGPL-3.0-or-later

package job

// This file is what a run says about itself while it is still going.
//
// Spec section 7's screen 4 asks for three things beside the stop button:
// progress, a live log, and statistics per port and per channel. The log was
// there; the progress line was not, and neither were the statistics — the bus
// declared events.RunProgress and nothing ever published one, so a run that
// was reading its tenth page still said «План составляется…» to the person
// watching it.

// Progress is how far a run has got and what it has cost so far.
//
// Counts rather than a percentage, and both halves of every pair: «43%» over a
// run that has failed every item it touched reads like progress, and the whole
// question a person is asking while they watch is whether to stop it.
type Progress struct {
	// Done is items finished, Total is items planned. Total is fixed when the
	// plan is written, so the pair is honest for the whole run — no estimate
	// moves under the person reading it.
	Done, Total int64

	// Items and Failed split Done into what was collected and what was lost.
	// Requests is what it has cost, which is not the same number: one item can
	// be several requests, and a retried one is several more.
	Items, Failed, Requests int64

	// Ports is one line per proxy port, when the run has a pool to ask. Empty
	// otherwise — a build that collects without one, or a run whose ports are
	// not open yet — and an empty table is rendered as «пока нечего показать»
	// rather than as a pool of no ports.
	Ports []PortStat
}

// PortStat is one port's line of that table.
//
// The channel travels with the port because that is the choice an operator can
// act on: a channel whose ports keep being quarantined is a channel to take
// out of the mix, and a port number alone does not say which one that is.
//
// A copy of blanktrail.PortReport rather than the type itself, so that this
// package — which knows nothing about proxies and is tested without one —
// does not take a dependency on the SDK to describe what it is publishing.
type PortStat struct {
	Port        int
	Channel     string
	Requests    int
	Quarantined bool
	Gone        bool
}
