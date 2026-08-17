// SPDX-License-Identifier: AGPL-3.0-or-later

// Package winres writes the object file that puts an icon on a Windows binary.
//
// The Go toolchain links any .syso file it finds beside a main package, which
// is the whole mechanism: a COFF object holding a resource section, produced
// here rather than by a tool the build would have to carry. The release
// workflow cross-compiles six platforms from one Linux runner with nothing but
// the go tool, and this is what lets an icon ship without changing that.
//
// The files it produces are committed, and a test regenerates them and compares
// — so a committed object can never quietly drift from the drawing that made
// it. Refresh them with: go test ./cmd/... -run Syso -update
//
// What it writes is the smallest thing that works: two sections, one holding
// the resource directory and one holding the images, and a relocation per image
// because the addresses in the directory are only known once the linker has
// placed the section.
package winres

import (
	"errors"
	"fmt"
	"sort"
)

// Machine is the architecture an object file is for.
//
// A COFF object carries the machine in its header and a relocation type that
// differs per architecture, so an object built for one refuses to link into
// another. The two here are the two Windows targets the release builds.
type Machine string

// The two Windows targets the release builds. Anything else is refused rather
// than written as one of these: an object with the wrong machine in its header
// fails at link time with a message about the object rather than about the
// architecture, which is a long way from the mistake.
const (
	AMD64 Machine = "amd64"
	ARM64 Machine = "arm64"
)

// machineCode and relocType are what the header and each relocation carry.
//
// The relocation is the "32-bit address without an image base" kind in each
// architecture's own numbering — the same meaning, three different numbers,
// which is exactly the sort of thing that is silently wrong until a linker
// complains a year later.
var machines = map[Machine]struct {
	code      uint16
	relocType uint16
}{
	AMD64: {0x8664, 0x0003}, // IMAGE_REL_AMD64_ADDR32NB
	ARM64: {0xAA64, 0x0002}, // IMAGE_REL_ARM64_ADDR32NB
}

// Icon is one image of an icon, at one size.
type Icon struct {
	Width, Height int
	// Data is what an RT_ICON resource holds: a BITMAPINFOHEADER, the colours
	// bottom-up, and the AND mask.
	Data []byte
}

// Resource type numbers, from the Windows headers.
const (
	rtIcon      = 3
	rtGroupIcon = 14
)

// language is what every resource here is filed under.
//
// Neutral rather than a real language: an icon has no words in it, and filing
// it under one would make it invisible to a system asking for another.
const language = 0

// SYSO writes the object file that gives a binary this icon.
func SYSO(machine Machine, icons []Icon) ([]byte, error) {
	if len(icons) == 0 {
		return nil, errors.New("winres: an icon with no images is not an icon")
	}
	spec, ok := machines[machine]
	if !ok {
		return nil, fmt.Errorf("winres: %q is not an architecture this writes for", machine)
	}
	for i, icon := range icons {
		if icon.Width < 1 || icon.Width > 256 || icon.Height < 1 || icon.Height > 256 {
			return nil, fmt.Errorf("winres: image %d is %dx%d; an icon is between 1 and 256",
				i, icon.Width, icon.Height)
		}
		if len(icon.Data) < 40 {
			return nil, fmt.Errorf("winres: image %d carries %d bytes, less than its own header",
				i, len(icon.Data))
		}
	}

	// The images are sorted largest first, which is the order icon editors
	// write and the order the shell reads fastest. The ids follow that order,
	// so the group's entries and the resources agree without a second sort.
	ordered := append([]Icon(nil), icons...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Width > ordered[j].Width })

	data, offsets := packImages(ordered)
	group := groupDirectory(ordered)
	groupOffset := len(data)
	data = append(data, group...)

	directory, relocations := buildDirectory(ordered, offsets, groupOffset, len(group))
	return writeObject(spec.code, spec.relocType, directory, data, relocations), nil
}

// packImages lays the images out one after another and says where each landed.
func packImages(icons []Icon) (data []byte, offsets []int) {
	for _, icon := range icons {
		offsets = append(offsets, len(data))
		data = append(data, icon.Data...)
		// Aligned to four, which is what the resource section wants of every
		// entry it points at.
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}
	return data, offsets
}

// groupDirectory is the RT_GROUP_ICON resource: the list of which sizes exist
// and which RT_ICON holds each.
//
// It is what the shell actually looks at. An executable with the images and no
// group has an icon nothing can find.
func groupDirectory(icons []Icon) []byte {
	out := make([]byte, 0, 6+14*len(icons))
	out = appendU16(out, 0)                  // reserved
	out = appendU16(out, 1)                  // type: icon
	out = appendU16(out, uint16(len(icons))) // how many

	for i, icon := range icons {
		// Zero means 256 in a byte that has to hold a width. The format is
		// older than the size.
		out = append(out, byte(icon.Width&0xff), byte(icon.Height&0xff))
		out = append(out, 0, 0) // colours in the palette, reserved
		out = appendU16(out, 1) // planes
		out = appendU16(out, 32)
		out = appendU32(out, uint32(len(icon.Data)))
		out = appendU16(out, uint16(i+1)) // the id of the RT_ICON holding it
	}
	return out
}

// dataEntry is where in the directory an image's address sits, and what it
// points at. The address is a relocation: the linker fills it once it knows
// where the section landed.
type dataEntry struct {
	at     int // offset of the entry inside the directory section
	offset int // offset of the bytes inside the data section
	size   int
}

// buildDirectory writes the three-level tree the resource section is: type,
// then id, then language, then an entry saying where the bytes are.
func buildDirectory(icons []Icon, offsets []int, groupOffset, groupSize int) ([]byte, []dataEntry) {
	// Sizes are worked out before anything is written, because every directory
	// holds the offset of the ones below it.
	const dirSize, entrySize, dataEntrySize = 16, 8, 16

	// Level 1: two types. Level 2: one directory per type. Level 3: one
	// language directory per resource.
	resources := len(icons) + 1
	level1 := dirSize + 2*entrySize
	level2 := (dirSize + len(icons)*entrySize) + (dirSize + 1*entrySize)
	level3 := resources * (dirSize + entrySize)
	dataEntriesAt := level1 + level2 + level3

	out := make([]byte, 0, dataEntriesAt+resources*dataEntrySize)
	var entries []dataEntry

	// Level 1: the two types, in ascending order — the shell binary-searches
	// these, so out of order is not merely untidy.
	out = appendDirHeader(out, 0, 2)
	iconsDirAt := level1
	groupDirAt := iconsDirAt + dirSize + len(icons)*entrySize
	out = appendDirEntry(out, rtIcon, iconsDirAt, true)
	out = appendDirEntry(out, rtGroupIcon, groupDirAt, true)

	// Level 2: the ids under each type.
	languageDirsAt := level1 + level2
	out = appendDirHeader(out, 0, len(icons))
	for i := range icons {
		out = appendDirEntry(out, i+1, languageDirsAt+i*(dirSize+entrySize), true)
	}
	out = appendDirHeader(out, 0, 1)
	out = appendDirEntry(out, 1, languageDirsAt+len(icons)*(dirSize+entrySize), true)

	// Level 3: one language under each id, pointing at the entry that holds
	// the address and the size.
	for i := range resources {
		out = appendDirHeader(out, 0, 1)
		out = appendDirEntry(out, language, dataEntriesAt+i*dataEntrySize, false)
	}

	// The entries themselves. The address field is written as the offset inside
	// the data section and a relocation is recorded for it: a COFF relocation of
	// this kind adds the section's own address to whatever is already there.
	for i, icon := range icons {
		entries = append(entries, dataEntry{at: len(out), offset: offsets[i], size: len(icon.Data)})
		out = appendU32(out, uint32(offsets[i]))
		out = appendU32(out, uint32(len(icon.Data)))
		out = appendU32(out, 0) // code page: none, these are not words
		out = appendU32(out, 0) // reserved
	}
	entries = append(entries, dataEntry{at: len(out), offset: groupOffset, size: groupSize})
	out = appendU32(out, uint32(groupOffset))
	out = appendU32(out, uint32(groupSize))
	out = appendU32(out, 0)
	out = appendU32(out, 0)

	return out, entries
}

func appendDirHeader(dst []byte, named, ids int) []byte {
	dst = appendU32(dst, 0) // characteristics
	dst = appendU32(dst, 0) // time stamp: zero, so two builds of one icon match
	dst = appendU16(dst, 0) // version, major and minor
	dst = appendU16(dst, 0)
	dst = appendU16(dst, uint16(named))
	return appendU16(dst, uint16(ids))
}

func appendDirEntry(dst []byte, id, offset int, subdirectory bool) []byte {
	dst = appendU32(dst, uint32(id))
	if subdirectory {
		// The high bit is how an entry says "what follows is another
		// directory" rather than "these are the bytes".
		return appendU32(dst, uint32(offset)|0x80000000)
	}
	return appendU32(dst, uint32(offset))
}

// writeObject assembles the COFF file around the two sections.
func writeObject(machine, relocType uint16, directory, data []byte, entries []dataEntry) []byte {
	const headerSize, sectionHeaderSize, relocSize, symbolSize = 20, 40, 10, 18

	sectionsAt := headerSize + 2*sectionHeaderSize
	directoryAt := sectionsAt
	dataAt := directoryAt + len(directory)
	relocationsAt := dataAt + len(data)
	symbolsAt := relocationsAt + len(entries)*relocSize

	out := make([]byte, 0, symbolsAt+2*symbolSize+4)

	// The header. Two sections, two symbols — one per section, which is what
	// the relocations point at.
	out = appendU16(out, machine)
	out = appendU16(out, 2)
	out = appendU32(out, 0) // time stamp: zero, so the file is reproducible
	out = appendU32(out, uint32(symbolsAt))
	out = appendU32(out, 2)
	out = appendU16(out, 0) // no optional header: this is an object, not an image
	out = appendU16(out, 0) // no characteristics

	const initialisedReadOnly = 0x40000040 // CNT_INITIALIZED_DATA | MEM_READ
	out = appendSectionHeader(out, ".rsrc$01", len(directory), directoryAt,
		relocationsAt, len(entries), initialisedReadOnly)
	out = appendSectionHeader(out, ".rsrc$02", len(data), dataAt, 0, 0, initialisedReadOnly)

	out = append(out, directory...)
	out = append(out, data...)

	// One relocation per resource, each pointing at the second section's
	// symbol. The offset already written into the field is the addend.
	for _, e := range entries {
		out = appendU32(out, uint32(e.at))
		out = appendU32(out, 1) // symbol index: the second section
		out = appendU16(out, relocType)
	}

	out = appendSymbol(out, ".rsrc$01", 1)
	out = appendSymbol(out, ".rsrc$02", 2)
	out = appendU32(out, 4) // the string table: its own size and nothing else

	return out
}

func appendSectionHeader(dst []byte, name string, size, at, relocationsAt, relocations, flags int) []byte {
	var padded [8]byte
	copy(padded[:], name)
	dst = append(dst, padded[:]...)

	dst = appendU32(dst, 0) // virtual size and address: an object has neither
	dst = appendU32(dst, 0)
	dst = appendU32(dst, uint32(size))
	dst = appendU32(dst, uint32(at))
	dst = appendU32(dst, uint32(relocationsAt))
	dst = appendU32(dst, 0) // line numbers: none
	dst = appendU16(dst, uint16(relocations))
	dst = appendU16(dst, 0)
	return appendU32(dst, uint32(flags))
}

// appendSymbol writes a section symbol: the name of the section, and the
// section it stands for.
func appendSymbol(dst []byte, name string, section int) []byte {
	var padded [8]byte
	copy(padded[:], name)
	dst = append(dst, padded[:]...)

	dst = appendU32(dst, 0) // value: the start of the section
	dst = appendU16(dst, uint16(section))
	dst = appendU16(dst, 0) // type: none
	dst = append(dst, 3)    // storage class: static
	return append(dst, 0)   // no auxiliary records
}

func appendU16(dst []byte, v uint16) []byte {
	return append(dst, byte(v), byte(v>>8))
}

func appendU32(dst []byte, v uint32) []byte {
	return append(dst, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
