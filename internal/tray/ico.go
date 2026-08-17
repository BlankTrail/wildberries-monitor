// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

// The same mark, in the shape an icon resource wants.
//
// The notification area is handed pixels through a bitmap; Explorer, the
// taskbar and the Alt-Tab list read a resource compiled into the file. Both
// come from markPixels, so there is one drawing and not two that drift.
//
// Two things differ from what the tray gets, and both are the format's rules
// rather than a choice. The rows run bottom to top, which is how a BMP has
// always been stored. And the colours are straight rather than premultiplied:
// Windows multiplies them itself when it loads an icon, and a file that arrived
// premultiplied comes out with a dark halo where the edge should be soft.

// IconSizes are the sizes an icon resource carries.
//
// Explorer picks by view — small icons, large icons, tiles — and the shell
// scales whatever is nearest when the size it wants is missing. These are the
// ones Windows actually asks for, and a file with only sixteen in it is the
// blurry icon on every view but one.
var IconSizes = []int{16, 20, 24, 32, 48, 64, 128, 256}

// IconImage is one size of the mark, as an icon resource stores it.
type IconImage struct {
	Width, Height int
	// Data is a BITMAPINFOHEADER, the colours bottom-up, and the AND mask.
	// Everything an RT_ICON resource holds, which is a BMP file without its
	// fourteen-byte file header.
	Data []byte
}

// IconImages draws the mark at every size an icon resource carries.
func IconImages() []IconImage {
	out := make([]IconImage, 0, len(IconSizes))
	for _, size := range IconSizes {
		out = append(out, IconImage{Width: size, Height: size, Data: iconImageData(size)})
	}
	return out
}

// iconImageData is one image: the header, the colours, and the mask.
func iconImageData(size int) []byte {
	pixels := markPixels(size)
	maskStride := ((size + 31) / 32) * 4 // rows padded to four bytes, as a BMP wants
	out := make([]byte, 0, 40+size*size*4+maskStride*size)

	// BITMAPINFOHEADER, with the height doubled: an icon's header describes the
	// colours and the mask as one image stacked on itself, which is the one
	// rule everybody gets wrong the first time.
	out = appendU32(out, 40)
	out = appendU32(out, uint32(int32(size)))
	out = appendU32(out, uint32(int32(size*2)))
	out = appendU16(out, 1)  // planes
	out = appendU16(out, 32) // bits per pixel
	out = appendU32(out, 0)  // no compression
	out = appendU32(out, uint32(size*size*4+maskStride*size))
	out = appendU32(out, 0) // pixels per metre, both axes
	out = appendU32(out, 0)
	out = appendU32(out, 0) // colours used, colours important
	out = appendU32(out, 0)

	// The colours, bottom row first, with the premultiplication undone.
	for y := size - 1; y >= 0; y-- {
		row := pixels[y*size*4 : (y+1)*size*4]
		for x := 0; x < len(row); x += 4 {
			out = append(out, straight(row[x], row[x+1], row[x+2], row[x+3])...)
		}
	}

	// The AND mask, all zeroes: with 32-bit colours the alpha channel is what
	// shapes the icon. It is still required — the header says the image is
	// twice as tall, and a file that stops early is a file the shell refuses.
	out = append(out, make([]byte, maskStride*size)...)
	return out
}

// straight undoes the premultiplication for one pixel.
//
// The tray needs premultiplied colours because the shell blends them itself;
// an icon resource needs the plain ones, because Windows multiplies them on
// load and doing it twice leaves a dark ring around everything soft.
func straight(b, g, r, a byte) []byte {
	if a == 0 {
		return []byte{0, 0, 0, 0}
	}
	unmix := func(c byte) byte {
		v := (int(c)*255 + int(a)/2) / int(a)
		if v > 255 {
			// Rounding can carry a channel past its alpha by one. Clamped
			// rather than left to wrap, which would turn a white edge black.
			return 255
		}
		return byte(v)
	}
	return []byte{unmix(b), unmix(g), unmix(r), a}
}

func appendU16(dst []byte, v uint16) []byte {
	return append(dst, byte(v), byte(v>>8))
}

func appendU32(dst []byte, v uint32) []byte {
	return append(dst, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
