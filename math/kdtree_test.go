// math/kdtree_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"testing"
)

func TestBuildKDTree(t *testing.T) {
	// Test empty input
	tree := BuildKDTree(nil)
	if tree != nil {
		t.Error("expected nil tree for nil input")
	}

	tree = BuildKDTree([]Point2LL{})
	if tree != nil {
		t.Error("expected nil tree for empty input")
	}

	// Test single point
	points := []Point2LL{{-75, 40}}
	tree = BuildKDTree(points)
	if tree == nil {
		t.Fatal("expected non-nil tree for single point")
	}
	if tree.Location != points[0] {
		t.Errorf("expected location %v, got %v", points[0], tree.Location)
	}
	if tree.Left != nil || tree.Right != nil {
		t.Error("expected nil children for single-point tree")
	}
}

func TestSelectDistributedPoints(t *testing.T) {
	// Test empty input
	result := SelectDistributedPoints(nil, 10)
	if len(result) != 0 {
		t.Errorf("expected empty map for nil input, got %d points", len(result))
	}

	// Test requesting more points than available
	points := []Point2LL{{-75, 40}, {-76, 41}}
	result = SelectDistributedPoints(points, 10)
	if len(result) != 2 {
		t.Errorf("expected 2 points when requesting more than available, got %d", len(result))
	}

	// Test requesting zero points
	result = SelectDistributedPoints(points, 0)
	if len(result) != 0 {
		t.Errorf("expected empty map for n=0, got %d points", len(result))
	}
}

func TestSelectDistributedPointsDistribution(t *testing.T) {
	// Create a grid of points
	var points []Point2LL
	for lon := float32(-80); lon <= -70; lon += 0.5 {
		for lat := float32(35); lat <= 45; lat += 0.5 {
			points = append(points, Point2LL{lon, lat})
		}
	}

	n := len(points) / 4 // select 25% of points
	result := SelectDistributedPoints(points, n)

	if len(result) < n/2 || len(result) > n*2 {
		t.Errorf("expected approximately %d points, got %d", n, len(result))
	}

	// Verify all selected points are from the original set
	pointSet := make(map[Point2LL]bool)
	for _, p := range points {
		pointSet[p] = true
	}
	for p := range result {
		if !pointSet[p] {
			t.Errorf("selected point %v not in original set", p)
		}
	}
}

func TestSelectByIndex(t *testing.T) {
	// Create a simple balanced tree
	points := []Point2LL{
		{-80, 35}, {-78, 36}, {-76, 37}, {-74, 38},
		{-72, 39}, {-70, 40}, {-68, 41}, {-66, 42},
	}
	tree := BuildKDTree(points)

	// Verify different indices return different points (mostly)
	seen := make(map[Point2LL]bool)
	for i := 0; i < 20; i++ {
		p := tree.selectByIndex(i)
		seen[p] = true
	}

	// Should have explored multiple distinct points
	if len(seen) < 4 {
		t.Errorf("selectByIndex only found %d unique points, expected at least 4", len(seen))
	}
}

func BenchmarkSelectDistributedPoints(b *testing.B) {
	// Create a large set of points similar to ARTCC coverage
	var points []Point2LL
	for lon := float32(-130); lon <= -65; lon += 0.05 {
		for lat := float32(25); lat <= 50; lat += 0.05 {
			points = append(points, Point2LL{lon, lat})
		}
	}

	n := len(points) / 10

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SelectDistributedPoints(points, n)
	}
}
