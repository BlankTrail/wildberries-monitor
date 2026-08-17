// SPDX-License-Identifier: AGPL-3.0-or-later

package winres

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

// The object file is bytes, so the tests read bytes — and where they can, they
// read them the way a linker does: debug/pe parses a COFF object, so the header,
// the sections, the relocations and the symbols are checked by the same code
// that has to accept this file for the icon to exist at all.

// sample is an icon with three sizes, each carrying a distinguishable payload.
func sample() []Icon {
	icon := func(size int, fill byte) Icon {
		data := make([]byte, 40+size*size*4)
		binary.LittleEndian.PutUint32(data, 40)
		for i := 40; i < len(data); i++ {
			data[i] = fill
		}
		return Icon{Width: size, Height: size, Data: data}
	}
	return []Icon{icon(16, 0x11), icon(32, 0x22), icon(48, 0x33)}
}

func TestSYSO_IsAnObjectFileALinkerAccepts(t *testing.T) {
	// The whole point of the file. Parsed by the same package the toolchain
	// uses to read one, so "it looks right" is not what is being claimed.
	for _, machine := range []Machine{AMD64, ARM64} {
		object, err := SYSO(machine, sample())
		if err != nil {
			t.Fatalf("%s: %v", machine, err)
		}

		f, err := pe.NewFile(bytes.NewReader(object))
		if err != nil {
			t.Fatalf("%s: не разбирается как COFF: %v", machine, err)
		}
		defer f.Close()

		if want := machines[machine].code; f.Machine != want {
			t.Errorf("%s: машина %#x, ожидалось %#x", machine, f.Machine, want)
		}
		if len(f.Sections) != 2 {
			t.Fatalf("%s: секций %d, ожидалось две", machine, len(f.Sections))
		}
		if f.Sections[0].Name != ".rsrc$01" || f.Sections[1].Name != ".rsrc$02" {
			t.Errorf("%s: секции названы %q и %q", machine, f.Sections[0].Name, f.Sections[1].Name)
		}
	}
}

func TestSYSO_RelocatesEveryAddressInTheDirectory(t *testing.T) {
	// The directory holds one address per resource and none of them is known
	// until the linker places the section. A missing relocation is an icon
	// pointing into nothing, which is a file that builds and shows the default.
	icons := sample()
	object, err := SYSO(AMD64, icons)
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	f, err := pe.NewFile(bytes.NewReader(object))
	if err != nil {
		t.Fatalf("pe.NewFile: %v", err)
	}
	defer f.Close()

	// One per image, plus the group directory that names them.
	if want := len(icons) + 1; len(f.Sections[0].Relocs) != want {
		t.Fatalf("перемещений %d, ожидалось %d", len(f.Sections[0].Relocs), want)
	}
	for i, r := range f.Sections[0].Relocs {
		if r.Type != machines[AMD64].relocType {
			t.Errorf("перемещение %d имеет тип %#x, ожидался %#x", i, r.Type, machines[AMD64].relocType)
		}
		// Every one points at the symbol standing for the second section: that
		// is what turns an offset inside the data into an address.
		if r.SymbolTableIndex != 1 {
			t.Errorf("перемещение %d указывает на символ %d", i, r.SymbolTableIndex)
		}
	}
}

func TestSYSO_TheArchitecturesDifferWhereTheyMust(t *testing.T) {
	// A COFF object carries the machine in its header and a relocation type in
	// its own numbering. The same bytes for both would link into one and be
	// refused by the other — or worse, accepted and relocated wrongly.
	amd, err := SYSO(AMD64, sample())
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	arm, err := SYSO(ARM64, sample())
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	if bytes.Equal(amd, arm) {
		t.Fatal("объекты для двух архитектур совпадают побайтно")
	}
}

func TestSYSO_IsTheSameBytesEveryTime(t *testing.T) {
	// The files are committed and a test compares them. A time stamp in the
	// header would make every run disagree with the last, and the check that
	// keeps the icon honest would become the check nobody can keep green.
	first, err := SYSO(AMD64, sample())
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	second, err := SYSO(AMD64, sample())
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("две сборки одного значка дали разные байты")
	}
}

func TestSYSO_CarriesEveryImageAndAGroupThatNamesThem(t *testing.T) {
	// An executable with the images and no group has an icon nothing can find:
	// the group is what the shell actually reads.
	icons := sample()
	object, err := SYSO(AMD64, icons)
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	f, err := pe.NewFile(bytes.NewReader(object))
	if err != nil {
		t.Fatalf("pe.NewFile: %v", err)
	}
	defer f.Close()

	data, err := f.Sections[1].Data()
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	for i, icon := range icons {
		if !bytes.Contains(data, icon.Data) {
			t.Errorf("изображение %d (%dx%d) не попало в файл", i, icon.Width, icon.Height)
		}
	}

	// The group is the tail of the data section: a header of six bytes and
	// fourteen per image.
	group := data[len(data)-(6+14*len(icons)):]
	if binary.LittleEndian.Uint16(group[2:]) != 1 {
		t.Error("группа объявлена не значком")
	}
	if got := binary.LittleEndian.Uint16(group[4:]); int(got) != len(icons) {
		t.Errorf("в группе %d изображений, ожидалось %d", got, len(icons))
	}
	// Largest first, and each entry naming the resource that holds it.
	for i := range icons {
		entry := group[6+14*i:]
		if id := binary.LittleEndian.Uint16(entry[12:]); int(id) != i+1 {
			t.Errorf("запись %d ссылается на ресурс %d", i, id)
		}
	}
	if width := group[6]; width != 48 {
		t.Errorf("первая запись группы %dx — изображения не по убыванию", width)
	}
}

func TestSYSO_TwoHundredAndFiftySixIsWrittenAsZero(t *testing.T) {
	// The format is older than the size: a width lives in one byte, and the
	// largest icon Windows has says so with a zero. Written as 256 it wraps to
	// zero anyway on some writers and to garbage on others.
	big := Icon{Width: 256, Height: 256, Data: make([]byte, 40+256*256*4)}
	binary.LittleEndian.PutUint32(big.Data, 40)

	object, err := SYSO(AMD64, []Icon{big})
	if err != nil {
		t.Fatalf("SYSO: %v", err)
	}
	f, err := pe.NewFile(bytes.NewReader(object))
	if err != nil {
		t.Fatalf("pe.NewFile: %v", err)
	}
	defer f.Close()

	data, _ := f.Sections[1].Data()
	group := data[len(data)-20:]
	if group[6] != 0 || group[7] != 0 {
		t.Errorf("256 записано как %d×%d, ожидалось 0×0", group[6], group[7])
	}
}

func TestSYSO_RefusesWhatCannotBeAnIcon(t *testing.T) {
	// Each of these produces a file that builds and shows the default icon,
	// which is the failure that takes longest to notice.
	for _, c := range []struct {
		name  string
		icons []Icon
	}{
		{"без изображений", nil},
		{"нулевой размер", []Icon{{Width: 0, Height: 32, Data: make([]byte, 64)}}},
		{"больше 256", []Icon{{Width: 512, Height: 512, Data: make([]byte, 64)}}},
		{"данных меньше заголовка", []Icon{{Width: 32, Height: 32, Data: []byte{1, 2, 3}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := SYSO(AMD64, c.icons); err == nil {
				t.Error("принято")
			}
		})
	}
}

func TestSYSO_RefusesAnArchitectureItCannotWriteFor(t *testing.T) {
	// Silently writing an amd64 object for something else is a link that fails
	// with a message about the object rather than about the architecture.
	if _, err := SYSO("riscv64", sample()); err == nil {
		t.Error("неизвестная архитектура принята")
	}
}

func TestFiles_AreNamedTheWayTheToolchainRecognises(t *testing.T) {
	// There is no flag and no build tag: a file ending in _windows_amd64.syso
	// is linked into a windows/amd64 build and left alone by every other, and
	// the name is the whole mechanism.
	files, err := Files("cmd/thing", sample())
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("файлов %d, ожидалось два", len(files))
	}
	for _, want := range []string{"icon_windows_amd64.syso", "icon_windows_arm64.syso"} {
		found := false
		for _, f := range files {
			if bytes.HasSuffix([]byte(f.Path), []byte(want)) {
				found = true
			}
		}
		if !found {
			t.Errorf("нет файла %s", want)
		}
	}
}

func TestCheckAndWrite_NoticeAFileThatDriftedFromTheDrawing(t *testing.T) {
	// This pair is the whole reason a generated file may be committed at all: a
	// release needs nothing but the go tool, and the price of that is a file on
	// disk that could quietly stop matching the code that made it. Untested,
	// the check is a green test that proves nothing.
	dir := t.TempDir()
	files, err := Files(dir, sample())
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	// Nothing written yet: the check has to say so rather than pass on a file
	// that is not there.
	if err := Check(files); err == nil {
		t.Error("проверка прошла, когда файлов ещё нет")
	}

	if err := Write(files); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Check(files); err != nil {
		t.Errorf("только что записанные файлы не сошлись: %v", err)
	}

	// One byte changed, which is what a drawing edited without regenerating
	// looks like from here.
	changed := append([]byte(nil), files[0].Bytes...)
	changed[len(changed)/2] ^= 0xff
	if err := os.WriteFile(files[0].Path, changed, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = Check(files)
	if err == nil {
		t.Fatal("разошедшийся файл принят")
	}
	// And says what to do about it, because the person reading this is one who
	// has just edited a drawing and does not know a .syso exists.
	if !strings.Contains(err.Error(), "-update") {
		t.Errorf("err = %v — не говорит, как перегенерировать", err)
	}
}
