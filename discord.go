// discord.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/mmp/vice/pkg/log"

	discord_client "github.com/hugolgst/rich-go/client"
)

// DiscordStatus encapsulates the user's current vice activity; if the user is not
// currently controlling, Callsign should be an empty string.
type DiscordStatus struct {
	TotalDepartures, TotalArrivals int
	Position                       string
	Start                          time.Time
}

// discord collects various variables related to the state of the discord
// connection / activity updates.
var discord struct {
	// mu should be held when reading from or writing to any of the other
	// fields in the structure.
	mu sync.Mutex
	// last reported status from the user
	status DiscordStatus
	// has the status changed since the last activity update sent to
	// discord?
	statusChanged   bool
	updaterLaunched bool
}

func SetDiscordStatus(s DiscordStatus, config *Config, lg *log.Logger) {
	discord.mu.Lock()
	defer discord.mu.Unlock()

	if s.TotalDepartures != discord.status.TotalDepartures ||
		s.TotalArrivals != discord.status.TotalArrivals ||
		s.Position != discord.status.Position ||
		s.Start != discord.status.Start {
		discord.statusChanged = true
	}

	// Record the current status even if we're not sending discord updates;
	// they may be enabled later.
	discord.status = s

	// Don't even launch the update goroutine if the user has asked to not
	// update their discord status.
	if !discord.updaterLaunched && !config.InhibitDiscordActivity.Load() {
		discord.updaterLaunched = true
		go updateDiscordStatus(config, lg)
	}
}

func updateDiscordStatus(config *Config, lg *log.Logger) {
	// Sign in to the Vice app on Discord
	discord_err := discord_client.Login("1158289394717970473")
	if discord_err != nil {
		lg.Warn("Discord RPC Error", slog.String("error", discord_err.Error()))
		return
	}
	lg.Info("Successfully logged into Discord")

	for {
		// Immediately make a copy of all of the values we need and release
		// the mutex quickly.
		discord.mu.Lock()
		status := discord.status
		changed := discord.statusChanged
		discord.statusChanged = false
		discord.mu.Unlock()

		// Skip updates if the user has disabled discord updates.
		if changed && !config.InhibitDiscordActivity.Load() {
			// Common discord_client.Activity initialization regardless of
			// whether we're connected or not.
			activity := discord_client.Activity{
				LargeImage: "towerlarge",
				LargeText:  "Vice ATC",
				Timestamps: &discord_client.Timestamps{
					Start: &status.Start,
				},
			}
			if status.Position == "" {
				// Disconnected
				activity.State = "In the main menu"
				activity.Details = "On Break"
			} else {
				activity.State = strconv.Itoa(status.TotalDepartures) + " departures" + " | " +
					strconv.Itoa(status.TotalArrivals) + " arrivals"
				activity.Details = "Controlling " + status.Position
			}

			if err := discord_client.SetActivity(activity); err != nil {
				lg.Error("Discord RPC Error: ", slog.String("error", err.Error()))
			} else {
				lg.Info("Updated Discord activity", slog.Any("activity", activity))
			}
		}

		// Rate limit updates; note that this may introduce a small lag
		// from receiving an update (if we haven't sent an update for a
		// while), but it's just a few seconds so no big deal..
		time.Sleep(5 * time.Second)
	}
}
