package main

import (
	"encoding/json"
	"testing"
)

// Settings saved when these windows were called panes must still be read.
func TestOldPaneSettingsStillRead(t *testing.T) {
	old := `{"Version":91,
	  "MessagesPane":{"AudioAlertSelection":"Radio Static","ContactTransmissionsAlert":true},
	  "FlightStripPane":{"FontSize":19,"DarkMode":true}}`
	var c ConfigNoSim
	if err := json.Unmarshal([]byte(old), &c); err != nil {
		t.Fatal(err)
	}
	if c.MessagesWindow == nil || c.MessagesWindow.AudioAlertSelection != "Radio Static" ||
		!c.MessagesWindow.ContactTransmissionsAlert {
		t.Errorf("messages window settings not read back: %+v", c.MessagesWindow)
	}
	if c.FlightStripWindow == nil || c.FlightStripWindow.FontSize != 19 || !c.FlightStripWindow.DarkMode {
		t.Errorf("flight strip settings not read back: %+v", c.FlightStripWindow)
	}
	b, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	json.Unmarshal(b, &back)
	if _, ok := back["MessagesPane"]; !ok {
		t.Error("written config no longer uses the MessagesPane name")
	}
	if _, ok := back["FlightStripPane"]; !ok {
		t.Error("written config no longer uses the FlightStripPane name")
	}
}
