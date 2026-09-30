// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"encoding/json"
	"testing"

	"github.com/mmp/vice/util"
)

func TestHistoricalSetting(t *testing.T) {
	for _, s := range []string{`true`, `false`, `"yes"`, `"no"`, `"\u0079es"`} {
		var h HistoricalSetting
		if err := json.Unmarshal([]byte(s), &h); err != nil {
			t.Fatal(err)
		}
		if bool(h) != (s == `true` || s == `"yes"` || s == `"\u0079es"`) {
			t.Fatal("wrong setting")
		}
		var e util.ErrorLogger
		util.CheckJSON[Group]([]byte(`{"historical_scenario":`+s+`}`), &e)
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
	}
	for _, s := range []string{`"maybe"`, `1`, `null`} {
		var h HistoricalSetting
		if json.Unmarshal([]byte(s), &h) == nil {
			t.Fatal("accepted invalid setting", s)
		}
	}
}
