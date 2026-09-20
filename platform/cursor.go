// platform/cursor.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"

	"github.com/mmp/vice/math"
)

// Cursor is a mouse cursor image created by the windowing backend.
type Cursor interface {
	// SetOverride makes this the OS cursor, replacing the one imgui would
	// otherwise show, until Window.ClearCursorOverride is called.
	SetOverride()

	// Destroy frees the resources associated with the cursor. It is safe to
	// call more than once.
	Destroy()
}

type curEntry struct {
	width, height int
	hotspot       [2]int
	size          int
	offset        int
}

// DecodeCUR decodes a .cur file's bytes, returning the image whose size is
// closest to targetSize along with its hotspot.
/*
.cur uses Little Endian encoding:

0-1: header. (0 is valid)
2-3: file type. (2 is cursor)
4-5: number of images.
Each image is 16 bytes, so offset for the 6 bytes up there and the previous images. (6 + 16 * i)
1: width.
2: height.
4-5: hotspot x.
6-7: hotspot y.
8-11: size.
12-15: offset.
*/
func DecodeCUR(data []byte, targetSize int) (*image.RGBA, [2]int, error) {
	if len(data) < 6 {
		return nil, [2]int{}, errors.New("cursor file truncated")
	}
	if binary.LittleEndian.Uint16(data[0:2]) != 0 { // check the validity of the header/ file.
		return nil, [2]int{}, errors.New("invalid cursor header")
	}
	fileType := binary.LittleEndian.Uint16(data[2:4])
	if fileType != 2 { // ensure this is a cursor and not an icon.
		return nil, [2]int{}, fmt.Errorf("unsupported cursor type %d", fileType)
	}
	count := int(binary.LittleEndian.Uint16(data[4:6])) // number of cursor images
	if count == 0 {
		return nil, [2]int{}, errors.New("cursor file has no images")
	}

	best := curEntry{}
	bestArea := -1
	bestScore := int(^uint(0) >> 1)
	for i := range count { // go over all the images in the file.
		entryOffset := 6 + i*16 // skip the first 6 bytes and each image is 16 bytes.
		if entryOffset+16 > len(data) {
			return nil, [2]int{}, fmt.Errorf("cursor entry %d is truncated", i)
		}
		width := int(data[entryOffset])
		height := int(data[entryOffset+1])
		if width == 0 {
			width = 256
		}
		if height == 0 {
			height = 256
		}
		hotspot := [2]int{
			int(binary.LittleEndian.Uint16(data[entryOffset+4 : entryOffset+6])),
			int(binary.LittleEndian.Uint16(data[entryOffset+6 : entryOffset+8])),
		}
		size := int(binary.LittleEndian.Uint32(data[entryOffset+8 : entryOffset+12]))
		offset := int(binary.LittleEndian.Uint32(data[entryOffset+12 : entryOffset+16]))
		if size <= 0 || offset < 0 || offset+size > len(data) {
			continue
		}
		area := width * height
		score := math.Abs(max(width, height) - targetSize)
		if score < bestScore || (score == bestScore && area > bestArea) { // Select image that's closest to the target size.
			bestArea = area
			bestScore = score
			best = curEntry{
				width:   width,
				height:  height,
				hotspot: hotspot,
				size:    size,
				offset:  offset,
			}
		}
	}
	if bestArea < 0 {
		return nil, [2]int{}, errors.New("cursor file has no valid images")
	}

	imageData := data[best.offset : best.offset+best.size]
	rgba, err := decodeCursorDIB(imageData)
	if err != nil {
		return nil, [2]int{}, err
	}
	return rgba, best.hotspot, nil
}

// Turn cursor bitmap into an image.RGBA
func decodeCursorDIB(data []byte) (*image.RGBA, error) {
	if len(data) < 40 {
		return nil, errors.New("cursor DIB header too small")
	}
	headerSize := int(binary.LittleEndian.Uint32(data[0:4]))
	if headerSize < 40 || headerSize > len(data) {
		return nil, fmt.Errorf("cursor DIB header size %d is invalid", headerSize)
	}

	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	heightTotal := int32(binary.LittleEndian.Uint32(data[8:12]))
	if width <= 0 || heightTotal == 0 {
		return nil, errors.New("cursor DIB has invalid dimensions")
	}
	topDown := heightTotal < 0
	heightAbs := int(math.Abs(heightTotal))
	if heightAbs%2 != 0 {
		return nil, errors.New("cursor DIB height is not even")
	}
	height := heightAbs / 2

	planes := binary.LittleEndian.Uint16(data[12:14])
	bitCount := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])
	if planes != 1 || compression != 0 {
		return nil, errors.New("cursor DIB uses unsupported format")
	}

	var clrUsed uint32
	if headerSize >= 36 {
		clrUsed = binary.LittleEndian.Uint32(data[32:36])
	}

	paletteEntries := 0
	if bitCount <= 8 {
		if clrUsed > 0 {
			paletteEntries = int(clrUsed)
		} else {
			paletteEntries = 1 << bitCount
		}
	}

	paletteOffset := headerSize
	paletteBytes := paletteEntries * 4
	if paletteOffset+paletteBytes > len(data) {
		return nil, errors.New("cursor palette is truncated")
	}
	palette := make([]color.RGBA, paletteEntries)
	for i := range paletteEntries {
		base := paletteOffset + i*4
		palette[i] = color.RGBA{
			R: data[base+2],
			G: data[base+1],
			B: data[base],
			A: 255,
		}
	}

	switch bitCount {
	case 32, 24, 8, 4, 1:
	default:
		return nil, fmt.Errorf("cursor DIB bit depth %d is unsupported", bitCount)
	}

	xorStride := ((bitCount*width + 31) / 32) * 4
	andStride := ((width + 31) / 32) * 4
	if xorStride <= 0 || andStride <= 0 {
		return nil, errors.New("cursor DIB has invalid stride")
	}
	xorSize := xorStride * height
	andSize := andStride * height
	pixelOffset := paletteOffset + paletteBytes
	if pixelOffset+xorSize+andSize > len(data) {
		return nil, errors.New("cursor DIB pixel data is truncated")
	}

	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		xorRow := pixelOffset + srcY*xorStride
		andRow := pixelOffset + xorSize + srcY*andStride
		for x := range width {
			maskByte := data[andRow+(x/8)]
			maskBit := (maskByte >> uint(7-(x%8))) & 1

			var r, g, b, a byte
			switch bitCount {
			case 32:
				idx := xorRow + x*4
				b = data[idx]
				g = data[idx+1]
				r = data[idx+2]
				a = data[idx+3]
			case 24:
				idx := xorRow + x*3
				b = data[idx]
				g = data[idx+1]
				r = data[idx+2]
				a = 255
			case 8:
				p := paletteColor(palette, int(data[xorRow+x]))
				r, g, b, a = p.R, p.G, p.B, 255
			case 4:
				idxByte := data[xorRow+(x/2)]
				if x%2 == 0 {
					p := paletteColor(palette, int(idxByte>>4))
					r, g, b, a = p.R, p.G, p.B, 255
				} else {
					p := paletteColor(palette, int(idxByte&0x0F))
					r, g, b, a = p.R, p.G, p.B, 255
				}
			case 1:
				idxByte := data[xorRow+(x/8)]
				idx := int((idxByte >> uint(7-(x%8))) & 1)
				p := paletteColor(palette, idx)
				r, g, b, a = p.R, p.G, p.B, 255
			}

			if maskBit == 1 {
				a = 0
			}
			rgba.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: a})
		}
	}
	return rgba, nil
}

// Safely access the palette color.
func paletteColor(palette []color.RGBA, idx int) color.RGBA {
	if idx < 0 || idx >= len(palette) {
		return color.RGBA{}
	}
	return palette[idx]
}
