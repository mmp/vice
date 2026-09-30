// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"encoding/json"
	"fmt"
)

// HistoricalSetting accepts a JSON boolean or the convenient yes/no spelling.
type HistoricalSetting bool

func (h *HistoricalSetting) UnmarshalJSON(b []byte) error {
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	switch value {
	case true, "yes":
		*h = true
	case false, "no":
		*h = false
	default:
		return fmt.Errorf("historical_scenario must be true, false, \"yes\", or \"no\"")
	}
	return nil
}
func (HistoricalSetting) CheckJSON(v any) bool {
	switch x := v.(type) {
	case bool:
		return true
	case string:
		return x == "yes" || x == "no"
	}
	return false
}
