package radar

import (
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/renderer"
)

type ScopeTransformations struct {
	ndcFromLatLong                       math.Matrix3
	ndcFromWindow                        math.Matrix3
	latLongFromWindow, windowFromLatLong math.Matrix3
}

// GetScopeTransformations returns a ScopeTransformations object
// corresponding to the specified radar scope center, range, and rotation
// angle.
func GetScopeTransformations(paneExtent math.Extent2D, magneticVariation float32, nmPerLongitude float32,
	center math.Point2LL, rangenm float32, rotationAngle float32) ScopeTransformations {
	width, height := paneExtent.Width(), paneExtent.Height()
	aspect := width / height
	ndcFromLatLong := math.Identity3x3().
		// Final orthographic projection including the effect of the
		// window's aspect ratio.
		Ortho(-aspect, aspect, -1, 1).
		// Account for magnetic variation and any user-specified rotation
		Rotate(-math.Radians(rotationAngle+magneticVariation)).
		// Scale based on range and nm per latitude / longitude
		Scale(nmPerLongitude/rangenm, math.NMPerLatitude/rangenm).
		// Translate to center point
		Translate(-center[0], -center[1])

	ndcFromWindow := math.Identity3x3().
		Translate(-1, -1).
		Scale(2/width, 2/height)

	latLongFromNDC := ndcFromLatLong.Inverse()
	latLongFromWindow := latLongFromNDC.PostMultiply(ndcFromWindow)
	windowFromLatLong := latLongFromWindow.Inverse()

	return ScopeTransformations{
		ndcFromLatLong:    ndcFromLatLong,
		ndcFromWindow:     ndcFromWindow,
		latLongFromWindow: latLongFromWindow,
		windowFromLatLong: windowFromLatLong,
	}
}

// LoadLatLongViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that latitude-longiture positions can be
// provided for subsequent vertices.
func (st *ScopeTransformations) LoadLatLongViewingMatrices(cb *renderer.CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromLatLong)
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// LoadWindowViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that window-coordinate positions can be
// provided for subsequent vertices.
func (st *ScopeTransformations) LoadWindowViewingMatrices(cb *renderer.CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromWindow)
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// WindowFromLatLongP transforms a point given in latitude-longitude
// coordinates to window coordinates, snapped to a pixel center.
func (st *ScopeTransformations) WindowFromLatLongP(p math.Point2LL) [2]float32 {
	pw := st.windowFromLatLong.TransformPoint(p)
	pw[0], pw[1] = float32(int(pw[0]+0.5))+0.5, float32(int(pw[1]+0.5))+0.5
	return pw
}

// LatLongFromWindowP transforms a point p in window coordinates to
// latitude-longitude.
func (st *ScopeTransformations) LatLongFromWindowP(p [2]float32) math.Point2LL {
	return st.latLongFromWindow.TransformPoint(p)
}

// NormalizedFromWindowP transforms a point p in window coordinates to
// normalized [0,1]^2 coordinates.
func (st *ScopeTransformations) NormalizedFromWindowP(p [2]float32) [2]float32 {
	pn := st.ndcFromWindow.TransformPoint(p) // [-1,1]
	return [2]float32{(pn[0] + 1) / 2, (pn[1] + 1) / 2}
}

// LatLongFromWindowV transforms a vector in window coordinates to a vector
// in latitude-longitude coordinates.
func (st *ScopeTransformations) LatLongFromWindowV(v [2]float32) math.Point2LL {
	return st.latLongFromWindow.TransformVector(v)
}

// PixelDistanceNM returns the space between adjacent pixels expressed in
// nautical miles.
func (st *ScopeTransformations) PixelDistanceNM(nmPerLongitude float32) float32 {
	ll := st.LatLongFromWindowV([2]float32{1, 0})
	return math.NMLength2LL(ll, nmPerLongitude)
}