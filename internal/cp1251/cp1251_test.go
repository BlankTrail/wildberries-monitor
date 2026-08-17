// SPDX-License-Identifier: AGPL-3.0-or-later

package cp1251

import "testing"

func TestTableRoundTripsEveryByteItDefines(t *testing.T) {
	// The table is written by hand, so it is checked against itself: every
	// byte the high half defines must encode back to itself. A typo that gave
	// two bytes the same rune, or shifted a row of the table by one, is
	// exactly what this catches.
	for i, r := range high {
		b := byte(0x80 + i)
		if r == 0 {
			continue // unassigned; see the table's own comment
		}
		got, err := Encode(string(r))
		if err != nil {
			t.Errorf("byte %#02x (%q): %v", b, r, err)
			continue
		}
		if len(got) != 1 || got[0] != b {
			t.Errorf("%q encoded to % x, want %#02x", r, got, b)
		}
	}
	for b := 0xC0; b <= 0xFF; b++ {
		r := rune(0x0410 + b - 0xC0)
		got, err := Encode(string(r))
		if err != nil {
			t.Errorf("byte %#02x (%q): %v", b, r, err)
			continue
		}
		if len(got) != 1 || got[0] != byte(b) {
			t.Errorf("%q encoded to % x, want %#02x", r, got, b)
		}
	}
	for b := 0x00; b < 0x80; b++ {
		got, err := Encode(string(rune(b)))
		if err != nil || len(got) != 1 || got[0] != byte(b) {
			t.Errorf("ASCII %#02x encoded to % x, %v", b, got, err)
		}
	}
}

func TestNoTwoBytesShareARune(t *testing.T) {
	// Two entries with the same rune would make the table lossy in one
	// direction and silently unreachable in the other, and the round-trip test
	// above would still pass for whichever entry won the map.
	defined := 0
	for _, r := range high {
		if r != 0 {
			defined++
		}
	}
	if len(byteOf) != defined {
		t.Errorf("the table defines %d runes but the reverse map holds %d; two bytes share a rune", defined, len(byteOf))
	}
}

func TestByteNineEightIsNotClaimedByAnyRune(t *testing.T) {
	// 0x98 is unassigned in windows-1251. A table that let some rune land
	// there would produce files that decode differently on different machines.
	for r, b := range byteOf {
		if b == 0x98 {
			t.Errorf("%q (U+%04X) maps to the unassigned byte 0x98", r, r)
		}
	}
}

func TestDecode_ReadsBackWhatEncodeWrote(t *testing.T) {
	// The two directions are one table, so the round trip is the check that
	// matters: a shifted row encodes and decodes consistently wrong, and only
	// comparing against known text catches that — hence the literal below.
	const text = "Куртка зимняя №5 — «Тёплая» ёлка™"
	b, err := Encode(text)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := Decode(b); got != text {
		t.Errorf("round trip gave %q, want %q", got, text)
	}
	// Pinned bytes, so the test is not the table agreeing with itself:
	// "Куртка" in windows-1251.
	want := []byte{0xCA, 0xF3, 0xF0, 0xF2, 0xEA, 0xE0}
	if got, _ := Encode("Куртка"); string(got) != string(want) {
		t.Errorf("«Куртка» = % x, want % x", got, want)
	}
}

func TestDecode_TurnsTheUnassignedByteIntoTheReplacementCharacter(t *testing.T) {
	// Decoding refuses nothing: one stray byte in a hundred thousand phrases
	// must not throw the file away, and the mark is visible in the row it
	// landed in.
	got := Decode([]byte{'a', 0x98, 'b'})
	if got != "a\uFFFDb" {
		t.Errorf("Decode = %q, want the replacement character in the middle", got)
	}
}

func TestDecodeIfNeeded_TellsTheTwoEncodingsApart(t *testing.T) {
	// The one the user never chooses. A phrase file saved by Russian Excel is
	// windows-1251; the same file saved by anything modern is UTF-8; both have
	// to arrive as the same phrases.
	const text = "платье женское"
	cp, err := Encode(text)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := DecodeIfNeeded(cp); got != text {
		t.Errorf("windows-1251 read as %q, want %q", got, text)
	}
	if got := DecodeIfNeeded([]byte(text)); got != text {
		t.Errorf("UTF-8 read as %q, want %q", got, text)
	}
	// The direction that must never be guessed wrong: Cyrillic in
	// windows-1251 is a run of bytes above 0xBF, which is not valid UTF-8, so
	// UTF-8 text is never decoded through the table by mistake.
	if got := DecodeIfNeeded([]byte("ёлка")); got != "ёлка" {
		t.Errorf("UTF-8 Cyrillic came back as %q", got)
	}
}
