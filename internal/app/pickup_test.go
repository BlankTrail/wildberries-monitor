// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlankTrail/wildberries-monitor/internal/geo"
)

// knownPoints puts a directory of points into the store, each already carrying
// the region code somebody paid a request for.
//
// Already carrying it, so that resolving needs no site at all: what these tests
// are about is the order, the counting and the deduplication, and those are the
// parts that changed when eighty-five groups stopped being asked one at a time.
func knownPoints(t *testing.T, a *App, dests map[int64]int64) {
	t.Helper()
	ctx := t.Context()

	var points []geo.Point
	for id := range dests {
		points = append(points, geo.Point{
			ID: id, Address: "г. Москва, улица, " + itoa64(id),
			Latitude: 55.75, Longitude: 37.61,
		})
	}
	slices.SortFunc(points, func(a, b geo.Point) int { return int(a.ID - b.ID) })

	places, _ := geo.Split(points)
	if _, err := a.Store.SavePickupDirectory(ctx, places, points); err != nil {
		t.Fatalf("SavePickupDirectory: %v", err)
	}
	for id, dest := range dests {
		if err := a.Store.SetPickupDest(ctx, id, dest); err != nil {
			t.Fatalf("SetPickupDest: %v", err)
		}
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func TestResolvePickup_TheAnswersComeBackInTheOrderTheGroupsWereAsked(t *testing.T) {
	// «Все региональные центры» is eighty-five groups, and asking them one
	// after another is eighty-five round trips in a row while somebody watches
	// a spinner. Asked several at a time they finish in whatever order the
	// site answers — and a preset that puts a differently shuffled list in the
	// box every time it is pressed is one nobody can check against last time.
	a := newApp(t)
	ctx := t.Context()

	knownPoints(t, a, map[int64]int64{
		1: -1100, 2: -1200, 3: -1300, 4: -1400, 5: -1500,
		6: -1600, 7: -1700, 8: -1800, 9: -1900, 10: -2000,
	})

	groups := [][]int64{{1}, {2}, {3}, {4}, {5}, {6}, {7}, {8}, {9}, {10}}
	got, err := a.resolvePickup(ctx, groups)
	if err != nil {
		t.Fatalf("resolvePickup: %v", err)
	}
	want := []int64{-1100, -1200, -1300, -1400, -1500, -1600, -1700, -1800, -1900, -2000}
	if !slices.Equal(got.Dests, want) {
		t.Errorf("коды пришли как %v, ожидались в порядке групп %v", got.Dests, want)
	}
	if got.Resolved != len(want) {
		t.Errorf("разобрано %d из %d", got.Resolved, len(want))
	}
}

func TestResolvePickup_OneCodeIsAddedOnce(t *testing.T) {
	// Two settlements can sit in one region, so two groups can end at one
	// code. Which of them wins must not depend on which request finished
	// first, which is exactly what a counter shared between workers would
	// make it depend on.
	a := newApp(t)
	ctx := t.Context()

	knownPoints(t, a, map[int64]int64{1: -1100, 2: -1100, 3: -1300})

	got, err := a.resolvePickup(ctx, [][]int64{{1}, {2}, {3}})
	if err != nil {
		t.Fatalf("resolvePickup: %v", err)
	}
	if !slices.Equal(got.Dests, []int64{-1100, -1300}) {
		t.Errorf("коды = %v, ожидались [-1100 -1300]", got.Dests)
	}
	if got.Resolved != 2 {
		t.Errorf("разобрано %d, ожидалось 2", got.Resolved)
	}
}

func TestResolvePickup_AGroupWithNothingBehindItIsCountedAsFailed(t *testing.T) {
	// A group whose every candidate is closed is a region the preset asked for
	// and could not deliver. Counted, because a total that quietly shrank
	// would look like a choice that worked.
	a := newApp(t)
	ctx := t.Context()
	knownPoints(t, a, map[int64]int64{1: -1100})

	// The second group names a point this directory does not have at all,
	// which is the case a stale preset produces.
	got, err := a.resolvePickup(ctx, [][]int64{{1}, {404}})
	if err != nil {
		t.Fatalf("resolvePickup: %v", err)
	}
	if got.Resolved != 1 {
		t.Errorf("разобрано %d, ожидалось 1", got.Resolved)
	}
	if got.Failed != 1 {
		t.Errorf("не разобрано %d, ожидалось 1", got.Failed)
	}
}

func TestResolvePickup_NothingIsAskedOfTheSiteForACodeAlreadyPaidFor(t *testing.T) {
	// The counter under the picker is «запросов к сайту», and a code that is
	// already in the directory costs none. Pressing the preset a second time
	// should be nearly free, which is what makes it safe to press.
	a := newApp(t)
	ctx := t.Context()
	knownPoints(t, a, map[int64]int64{1: -1100, 2: -1200})

	got, err := a.resolvePickup(ctx, [][]int64{{1}, {2}})
	if err != nil {
		t.Fatalf("resolvePickup: %v", err)
	}
	if got.Asked != 0 {
		t.Errorf("сделано %d запросов к сайту за уже известные коды", got.Asked)
	}
}

func TestFanOut_WorksSeveralAtATime(t *testing.T) {
	// «Все региональные центры» is eighty-five round trips. One at a time they
	// are eighty-five waits in a row, and the only thing on screen is a
	// spinner — which is what «процесс длится очень долго» was.
	var live, most atomic.Int64
	release := make(chan struct{})

	done := make(chan []answer)
	go func() {
		done <- fanOut(context.Background(), 20, 8, func(i int) answer {
			n := live.Add(1)
			for {
				top := most.Load()
				if n <= top || most.CompareAndSwap(top, n) {
					break
				}
			}
			<-release
			live.Add(-1)
			return answer{dest: int64(-1000 - i)}
		})
	}()

	// Nothing finishes until the count has had a chance to climb.
	deadline := time.Now().Add(3 * time.Second)
	for most.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	got := <-done

	if most.Load() < 2 {
		t.Errorf("одновременно работало не больше %d — опрос остался последовательным", most.Load())
	}
	if most.Load() > 8 {
		t.Errorf("одновременно работало %d при пределе 8", most.Load())
	}
	// And the answers are still in the order of the work.
	for i, a := range got {
		if a.dest != int64(-1000-i) {
			t.Fatalf("ответ %d = %d — порядок потерян", i, a.dest)
		}
	}
}

func TestMergeAnswers_CountsWhatTheSiteWasAskedFor(t *testing.T) {
	// The number under the picker is «запросов к сайту», and it is what tells
	// somebody that pressing the preset a second time is nearly free. Summed
	// from the workers rather than kept in a shared counter, so it has to be
	// summed at all.
	got, err := mergeAnswers([]answer{
		{dest: -1100, asked: 2, tried: 2},
		{dest: -1200, asked: 1, tried: 1},
		{dest: 0, asked: 3, tried: 3},
	})
	if err != nil {
		t.Fatalf("mergeAnswers: %v", err)
	}
	if got.Asked != 6 {
		t.Errorf("запросов %d, ожидалось 6", got.Asked)
	}
	if got.Tried != 6 {
		t.Errorf("попыток %d, ожидалось 6", got.Tried)
	}
	if got.Resolved != 2 || got.Failed != 1 {
		t.Errorf("разобрано %d, не разобрано %d", got.Resolved, got.Failed)
	}
}
