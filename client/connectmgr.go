// client/connectmgr.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"errors"
	"log/slog"
	"net"
	"net/rpc"
	"strconv"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform/audio"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type ConnectionManager struct {
	localServerChan         chan *Server
	lastRemoteServerAttempt time.Time
	remoteSimServerChan     chan *serverConnection

	serverRPCVersionMismatch bool
	// localOnly suppresses connecting to the public vice server; the
	// facility engineering tool works only against local scenarios.
	localOnly bool

	lastRunningSimsUpdate  time.Time
	updateRunningSimsCall  *pendingCall
	updateRunningSimsError error

	LocalServer   *Server
	RemoteServer  *Server
	serverAddress string

	client              *ControlClient
	connectionStartTime time.Time
	ttsEnabled          func() bool // Evaluated per command so the user's setting can change at runtime

	onNewClient func(*ControlClient)
	onError     func(error)
}

func MakeServerManager(serverAddress string, overrides scenario.OverrideFiles, ttsEnabled func() bool, lg *log.Logger,
	onNewClient func(*ControlClient), onError func(error)) (*ConnectionManager, util.ErrorLogger, string) {
	cm := &ConnectionManager{
		serverAddress:           serverAddress,
		lastRemoteServerAttempt: time.Now(),
		remoteSimServerChan:     TryConnectRemoteServer(serverAddress, lg),
		ttsEnabled:              ttsEnabled,
		onNewClient:             onNewClient,
		onError:                 onError,
	}
	errorLogger, overrideErrors := cm.launchLocalServer(serverAddress, overrides, lg)
	return cm, errorLogger, overrideErrors
}

// MakeLocalServerManager is MakeServerManager without the connection to the
// public vice server: the facility engineering tool works entirely against
// the scenarios on the local disk, so attempting (and periodically retrying)
// a network connection would be pure noise.
func MakeLocalServerManager(overrides scenario.OverrideFiles, ttsEnabled func() bool, lg *log.Logger,
	onNewClient func(*ControlClient), onError func(error)) (*ConnectionManager, util.ErrorLogger, string) {
	cm := &ConnectionManager{
		localOnly:   true,
		ttsEnabled:  ttsEnabled,
		onNewClient: onNewClient,
		onError:     onError,
	}
	errorLogger, overrideErrors := cm.launchLocalServer("", overrides, lg)
	return cm, errorLogger, overrideErrors
}

func (cm *ConnectionManager) launchLocalServer(serverAddress string, overrides scenario.OverrideFiles,
	lg *log.Logger) (util.ErrorLogger, string) {
	rpcPort, errorLogger, overrideErrors := server.LaunchServerAsync(server.LaunchConfig{
		Overrides:     overrides,
		ServerAddress: serverAddress,
		IsLocal:       true,
	}, lg)

	if !errorLogger.HaveErrors() {
		client, err := getClient(net.JoinHostPort("localhost", strconv.Itoa(rpcPort)), lg)
		if err != nil {
			errorLogger.Error(err)
		} else {
			var cr server.ConnectResult
			if err := client.CallWithTimeout(server.ConnectRPC, server.ViceRPCVersion, &cr); err != nil {
				errorLogger.Error(err)
			} else {
				cm.LocalServer = &Server{
					RPCClient:             client,
					AvailableWXByFacility: cr.AvailableWXByFacility,
					name:                  "Local (Single controller)",
					catalogs:              cr.ScenarioCatalogs,
					runningSims:           cr.RunningSims,
				}
			}
		}
	}

	return errorLogger, overrideErrors
}

// dropRemoteServer closes the remote server connection and forgets it, so the
// main loop reconnects. Closing matters: without it the abandoned client keeps
// its socket open, and the server holds the connection until an EOF that never
// arrives.
func (cm *ConnectionManager) dropRemoteServer() {
	if cm.RemoteServer != nil {
		cm.RemoteServer.Close()
		cm.RemoteServer = nil
	}
}

func (cm *ConnectionManager) LoadLocalSim(s *sim.Sim, initials string, lg *log.Logger) (*ControlClient, error) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	var result server.NewSimResult
	req := server.AddLocalRequest{Sim: s, Initials: initials}
	if err := cm.LocalServer.Call(server.AddLocalRPC, &req, &result); err != nil {
		return nil, err
	}

	cm.client = NewControlClient(*result.SimState, result.ControllerToken, cm.ttsEnabled, initials,
		cm.LocalServer.RPCClient, lg)
	cm.connectionStartTime = time.Now()

	// Set remote server for STT log reporting (local sims report to remote server)
	if cm.RemoteServer != nil {
		cm.client.SetRemoteServer(cm.RemoteServer.RPCClient)
	}

	return cm.client, nil
}

func (cm *ConnectionManager) CreateNewSim(config server.NewSimRequest, initials string, srv *Server, lg *log.Logger) error {
	var result server.NewSimResult

	if err := srv.CallWithTimeout(server.NewSimRPC, config, &result); err != nil {
		err = TryDecodeError(err)
		if err == server.ErrRPCTimeout || err == server.ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			cm.dropRemoteServer()
		}
		return err
	} else {
		cm.handleSuccessfulConnection(result, srv, initials, lg)
		return nil
	}
}

// handleSuccessfulConnection handles the common logic for setting up a client
// connection after a successful RPC call to create or join a sim
func (cm *ConnectionManager) handleSuccessfulConnection(result server.NewSimResult, srv *Server,
	initials string, lg *log.Logger) {
	if cm.client != nil {
		cm.client.Disconnect()
	}

	cm.client = NewControlClient(*result.SimState, result.ControllerToken, cm.ttsEnabled, initials,
		srv.RPCClient, lg)

	cm.connectionStartTime = time.Now()

	if cm.onNewClient != nil {
		cm.onNewClient(cm.client)
	}
}

func (cm *ConnectionManager) Connected() bool {
	return cm.client != nil
}

func (cm *ConnectionManager) ConnectionStartTime() time.Time {
	if cm.client == nil {
		return time.Time{}
	} else {
		return cm.connectionStartTime
	}
}

func (cm *ConnectionManager) ClientIsLocal() bool {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	return cm.client != nil && cm.client.RPCClient() == cm.LocalServer.RPCClient
}

func (cm *ConnectionManager) Disconnect() {
	if cm.client != nil {
		cm.client.Disconnect()
		cm.client = nil
		if cm.onNewClient != nil {
			cm.onNewClient(nil)
		}
	}
}

func (cm *ConnectionManager) UpdateRunningSims() error {
	if cm.updateRunningSimsCall != nil && cm.updateRunningSimsCall.CheckFinished() {
		cm.updateRunningSimsCall.InvokeCallback(nil)
		cm.updateRunningSimsCall = nil
		err := cm.updateRunningSimsError
		cm.updateRunningSimsError = nil
		return err
	} else if time.Since(cm.lastRunningSimsUpdate) > 2*time.Second &&
		cm.RemoteServer != nil && cm.updateRunningSimsCall == nil {
		cm.lastRunningSimsUpdate = time.Now()

		var rs map[string]*server.RunningSim
		cm.updateRunningSimsError = nil
		cm.updateRunningSimsCall = makeRPCCall(cm.RemoteServer.Go(server.GetRunningSimsRPC, 0, &rs, nil),
			func(err error) {
				if err == nil {
					if cm.RemoteServer != nil {
						cm.RemoteServer.setRunningSims(rs)
					}
				} else {
					cm.updateRunningSimsError = err

					// Drop the server if we've lost the connection; the
					// main loop will attempt to reconnect.
					if util.IsRPCServerError(err) {
						cm.dropRemoteServer()
					}
				}
			})
	}
	return nil
}

func (cm *ConnectionManager) ConnectToSim(config server.JoinSimRequest, initials string, srv *Server, lg *log.Logger) error {
	var result server.NewSimResult
	if err := srv.CallWithTimeout(server.ConnectToSimRPC, config, &result); err != nil {
		err = TryDecodeError(err)
		if err == server.ErrRPCTimeout || err == server.ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			cm.dropRemoteServer()
		}
		return err
	} else {
		cm.handleSuccessfulConnection(result, srv, initials, lg)
		return nil
	}
}

func (cm *ConnectionManager) Update(p audio.Engine, lg *log.Logger) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	select {
	case remoteServerConn := <-cm.remoteSimServerChan:
		if err := remoteServerConn.Err; err != nil {
			lg.Info("Unable to connect to remote server", slog.Any("error", err))

			if err.Error() == server.ErrRPCVersionMismatch.Error() {
				cm.serverRPCVersionMismatch = true
				if cm.onError != nil {
					cm.onError(server.ErrRPCVersionMismatch)
				}
			}
			cm.RemoteServer = nil
		} else {
			cm.RemoteServer = remoteServerConn.Server
			// Update existing client with remote server for STT log reporting
			if cm.client != nil && cm.ClientIsLocal() {
				cm.client.SetRemoteServer(cm.RemoteServer.RPCClient)
			}
			// Set crash report client so crashes are reported to the remote server
			lg.SetCrashReportClient(cm.RemoteServer.RPCClient.Client)
		}

	default:
	}

	if cm.RemoteServer == nil && !cm.localOnly && time.Since(cm.lastRemoteServerAttempt) > 10*time.Second && !cm.serverRPCVersionMismatch {
		cm.lastRemoteServerAttempt = time.Now()
		cm.remoteSimServerChan = TryConnectRemoteServer(cm.serverAddress, lg)
	}

	if cm.client != nil {
		client := cm.client
		client.GetUpdates(p,
			func(err error) {
				client.PostEvent(sim.Event{
					Type:        sim.StatusMessageEvent,
					WrittenText: "Error getting update from server: " + err.Error(),
				})
				if err == server.ErrRPCTimeout || util.IsRPCServerError(err) {
					// Disconnect (and its sign-off attempt) first: for a
					// multi-controller sim cm.client shares this connection with
					// the remote server, which dropRemoteServer then closes.
					if cm.client != nil {
						cm.client.Disconnect()
						cm.client = nil
					}
					cm.dropRemoteServer()
					if cm.onNewClient != nil {
						cm.onNewClient(nil)
					}
					if cm.onError != nil {
						cm.onError(server.ErrServerDisconnected)
					}
				} else if cm.onError != nil {
					cm.onError(err)
				}
			})
	}
}
