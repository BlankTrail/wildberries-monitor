// SPDX-License-Identifier: AGPL-3.0-or-later

package winres

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// Generated is one object file: which architecture it is for and where it goes.
type Generated struct {
	Path  string
	Bytes []byte
}

// Files is the set of object files a main package needs for its icon.
//
// One per Windows architecture the release builds, named the way the go tool
// recognises them: a file ending in _windows_amd64.syso is linked into a
// windows/amd64 build and left alone by every other. That naming is the whole
// mechanism — there is no flag and no tag.
func Files(dir string, icons []Icon) ([]Generated, error) {
	out := make([]Generated, 0, len(machines))
	for _, machine := range []Machine{AMD64, ARM64} {
		object, err := SYSO(machine, icons)
		if err != nil {
			return nil, err
		}
		out = append(out, Generated{
			Path:  filepath.Join(dir, "icon_windows_"+string(machine)+".syso"),
			Bytes: object,
		})
	}
	return out, nil
}

// Check reports whether what is on disk is what the drawing produces now.
//
// The object files are committed rather than built, so that a release needs
// nothing but the go tool — and a committed file is a file that can drift from
// the code that made it. This is what makes that impossible to do quietly.
func Check(files []Generated) error {
	for _, f := range files {
		on, err := os.ReadFile(f.Path)
		if err != nil {
			return fmt.Errorf("%s: %w (перегенерировать: go test ./cmd/... -run Syso -update)", f.Path, err)
		}
		if !bytes.Equal(on, f.Bytes) {
			return fmt.Errorf("%s разошёлся с рисунком, из которого сделан "+
				"(перегенерировать: go test ./cmd/... -run Syso -update)", f.Path)
		}
	}
	return nil
}

// Write puts the object files on disk.
func Write(files []Generated) error {
	for _, f := range files {
		if err := os.WriteFile(f.Path, f.Bytes, 0o644); err != nil {
			return err
		}
	}
	return nil
}
