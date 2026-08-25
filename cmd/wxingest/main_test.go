package main

import (
	"fmt"
	"testing"
)

func TestShardOwnsPartition(t *testing.T) {
	defer func(idx, count int) { taskIndex, taskCount = idx, count }(taskIndex, taskCount)

	// With a single task, everything is owned.
	taskIndex, taskCount = 0, 1
	if !shardOwns("scrape/WX/PHL/2026-08-25T12:00:00Z.gob") {
		t.Error("single task must own all work")
	}

	// With multiple tasks, each key is owned by exactly one task and the
	// keys are reasonably balanced across tasks.
	taskCount = 4
	const nKeys = 10000
	counts := make(map[int]int)
	for i := range nKeys {
		key := fmt.Sprintf("scrape/WX/PHL/%d.gob", i)
		owners := 0
		for taskIndex = range taskCount {
			if shardOwns(key) {
				owners++
				counts[taskIndex]++
			}
		}
		if owners != 1 {
			t.Fatalf("%s owned by %d tasks", key, owners)
		}
	}
	for idx, n := range counts {
		if n < nKeys/taskCount/2 {
			t.Errorf("task %d owns only %d of %d keys; poor balance", idx, n, nKeys)
		}
	}
}
