// pkg/renderer/rgb.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"github.com/mmp/vice/pkg/math"
)

///////////////////////////////////////////////////////////////////////////
// RGB

type RGB struct {
	R, G, B float32
}

type RGBA struct {
	R, G, B, A float32
}

func LerpRGB(x float32, a, b RGB) RGB {
	return RGB{R: math.Lerp(x, a.R, b.R), G: math.Lerp(x, a.G, b.G), B: math.Lerp(x, a.B, b.B)}
}

func (r RGB) Equals(other RGB) bool {
	return r.R == other.R && r.G == other.G && r.B == other.B
}

func (r RGB) Scale(v float32) RGB {
	return RGB{R: r.R * v, G: r.G * v, B: r.B * v}
}

// RGBFromHex converts a packed integer color value to an RGB where the low
// 8 bits give blue, the next 8 give green, and then the next 8 give red.
func RGBFromHex(c int) RGB {
	r, g, b := (c>>16)&255, (c>>8)&255, c&255
	return RGB{R: float32(r) / 255, G: float32(g) / 255, B: float32(b) / 255}
}

func RGBFromUInt8(r uint8, g uint8, b uint8) RGB {
	return RGB{R: float32(r) / 255, G: float32(g) / 255, B: float32(b) / 255}
}
