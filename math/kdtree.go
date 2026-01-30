// math/kdtree.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"slices"
)

// KDNode is a node in a 2D KD-tree for Point2LL
type KDNode struct {
	Location Point2LL
	Left     *KDNode
	Right    *KDNode
}

// BuildKDTree constructs a balanced KD-tree from a slice of points.
// The tree alternates splitting by X (longitude) and Y (latitude) at each level.
func BuildKDTree(points []Point2LL) *KDNode {
	if len(points) == 0 {
		return nil
	}
	return buildKDTreeRecursive(points, 0)
}

func buildKDTreeRecursive(points []Point2LL, depth int) *KDNode {
	if len(points) == 0 {
		return nil
	}
	if len(points) == 1 {
		return &KDNode{Location: points[0]}
	}

	// Alternate between X (depth even) and Y (depth odd)
	axis := depth % 2

	// Sort by the splitting axis and find median
	slices.SortFunc(points, func(a, b Point2LL) int {
		if a[axis] < b[axis] {
			return -1
		} else if a[axis] > b[axis] {
			return 1
		}
		return 0
	})

	median := len(points) / 2

	return &KDNode{
		Location: points[median],
		Left:     buildKDTreeRecursive(points[:median], depth+1),
		Right:    buildKDTreeRecursive(points[median+1:], depth+1),
	}
}

// selectByIndex walks the tree using the bits of index to navigate.
// At each level, bit 0 decides left (0) or right (1), then shift right.
// This gives well-distributed traversal: 0→root, 1→right, 2→left, 3→right-right, etc.
func (tree *KDNode) selectByIndex(index int) Point2LL {
	if tree == nil {
		return Point2LL{}
	}

	node := tree
	for index != 0 {
		// Use low bit to decide direction
		if index&1 == 0 {
			if node.Left != nil {
				node = node.Left
			}
		} else {
			if node.Right != nil {
				node = node.Right
			}
		}
		index >>= 1

		// Stop if we've reached a leaf
		if node.Left == nil && node.Right == nil {
			break
		}
	}

	return node.Location
}

// SelectDistributedPoints selects n well-distributed points from a set
// using KD-tree partitioning and index-based traversal.
// Returns a map of the selected points for O(1) lookup.
func SelectDistributedPoints(points []Point2LL, n int) map[Point2LL]bool {
	if n <= 0 || len(points) == 0 {
		return make(map[Point2LL]bool)
	}
	if n >= len(points) {
		// Return all points
		result := make(map[Point2LL]bool, len(points))
		for _, p := range points {
			result[p] = true
		}
		return result
	}

	// Check if region crosses date line and needs longitude shifting.
	// If the direct longitude span is > 180°, the region crosses ±180°
	// and we need to shift coordinates so the KD-tree doesn't split there.
	minLon, maxLon := float32(180), float32(-180)
	for _, p := range points {
		if p[0] < minLon {
			minLon = p[0]
		}
		if p[0] > maxLon {
			maxLon = p[0]
		}
	}

	lonShift := float32(0)
	if maxLon-minLon > 180 {
		// Shift longitudes by 180° to move the split point away from the data
		lonShift = 180
	}

	// Make a copy with shifted coordinates
	pointsCopy := make([]Point2LL, len(points))
	for i, p := range points {
		lon := p[0] + lonShift
		if lon > 180 {
			lon -= 360
		} else if lon < -180 {
			lon += 360
		}
		pointsCopy[i] = Point2LL{lon, p[1]}
	}

	tree := BuildKDTree(pointsCopy)

	result := make(map[Point2LL]bool, n)

	// Use index bits to select points - the bit pattern naturally
	// explores different branches of the tree
	// We may need more iterations than n if we hit duplicates
	for i := 0; len(result) < n && i < n*3; i++ {
		p := tree.selectByIndex(i)
		// Shift longitude back to original coordinates
		lon := p[0] - lonShift
		if lon > 180 {
			lon -= 360
		} else if lon < -180 {
			lon += 360
		}
		result[Point2LL{lon, p[1]}] = true
	}

	return result
}
