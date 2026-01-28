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
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type ConnectionManager struct {
	localServerChan         chan *Server
	lastRemoteServerAttempt time.Time
	remoteSimServerChan     chan *serverConnection

	serverRPCVersionMismatch bool

	lastRunningSimsUpdate  time.Time
	updateRunningSimsCall  *pendingCall
	updateRunningSimsError error

	LocalServer   *Server
	RemoteServer  *Server
	serverAddress string

	client              *ControlClient
	connectionStartTime time.Time
	disableTTSPtr       *bool // Pointer to config's DisableTextToSpeech for runtime toggle

	onNewClient func(*ControlClient)
	onError     func(error)
}

func MakeServerManager(serverAddress, additionalScenario, additionalVideoMap string, disableTTSPtr *bool, lg *log.Logger,
	onNewClient func(*ControlClient), onError func(error)) (*ConnectionManager, util.ErrorLogger, string) {
	cm := &ConnectionManager{
		serverAddress:           serverAddress,
		lastRemoteServerAttempt: time.Now(),
		remoteSimServerChan:     TryConnectRemoteServer(serverAddress, lg),
		disableTTSPtr:           disableTTSPtr,
		onNewClient:             onNewClient,
		onError:                 onError,
	}

	// Launch local server
	rpcPort, errorLogger, extraScenarioErrors := server.LaunchServerAsync(server.ServerLaunchConfig{
		ExtraScenario: additionalScenario,
		ExtraVideoMap: additionalVideoMap,
		ServerAddress: serverAddress,
		IsLocal:       true,
	}, lg)

	if !errorLogger.HaveErrors() {
		client, err := getClient(net.JoinHostPort("localhost", strconv.Itoa(rpcPort)), lg)
		if err != nil {
			errorLogger.Error(err)
		} else {
			var cr server.ConnectResult
			if err := client.callWithTimeout(server.ConnectRPC, server.ViceRPCVersion, &cr); err != nil {
				errorLogger.Error(err)
			} else {
				cm.LocalServer = &Server{
					RPCClient:           client,
					HaveTTS:             cr.HaveTTS,
					AvailableWXByTRACON: cr.AvailableWXByTRACON,
					name:                "Local (Single controller)",
					catalogs:            cr.ScenarioCatalogs,
					runningSims:         cr.RunningSims,
				}
			}
		}
	}

	return cm, errorLogger, extraScenarioErrors
}

func (cm *ConnectionManager) LoadLocalSim(s *sim.Sim, initials string, lg *log.Logger) (*ControlClient, error) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	var result server.NewSimResult
	if err := cm.LocalServer.Call(server.AddLocalRPC, s, &result); err != nil {
		return nil, err
	}

	cm.client = NewControlClient(*result.SimState, result.ControllerToken, cm.LocalServer.HaveTTS, cm.disableTTSPtr, initials, cm.LocalServer.RPCClient, lg)
	cm.connectionStartTime = time.Now()

	// Set remote server for STT log reporting (local sims report to remote server)
	if cm.RemoteServer != nil {
		cm.client.SetRemoteServer(cm.RemoteServer.RPCClient)
	}

	return cm.client, nil
}

func (cm *ConnectionManager) CreateNewSim(config server.NewSimRequest, initials string, srv *Server, lg *log.Logger) error {
	var result server.NewSimResult

	if err := srv.callWithTimeout(server.NewSimRPC, config, &result); err != nil {
		err = server.TryDecodeError(err)
		if err == server.ErrRPCTimeout || err == server.ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			cm.RemoteServer = nil
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

	cm.client = NewControlClient(*result.SimState, result.ControllerToken, srv.HaveTTS, cm.disableTTSPtr, initials, srv.RPCClient, lg)

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
		cm.updateRunningSimsCall.InvokeCallback(nil, nil)
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

					// nil out the server if we've lost the connection; the
					// main loop will attempt to reconnect.
					if util.IsRPCServerError(err) {
						cm.RemoteServer = nil
					}
				}
			})
	}
	return nil
}

func (cm *ConnectionManager) ConnectToSim(config server.JoinSimRequest, initials string, srv *Server, lg *log.Logger) error {
	var result server.NewSimResult
	if err := srv.callWithTimeout(server.ConnectToSimRPC, config, &result); err != nil {
		err = server.TryDecodeError(err)
		if err == server.ErrRPCTimeout || err == server.ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			cm.RemoteServer = nil
		}
		return err
	} else {
		cm.handleSuccessfulConnection(result, srv, initials, lg)
		return nil
	}
}

func (cm *ConnectionManager) Update(es *sim.EventStream, p platform.Platform, lg *log.Logger) {
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
		}

	default:
	}

	if cm.RemoteServer == nil && time.Since(cm.lastRemoteServerAttempt) > 10*time.Second && !cm.serverRPCVersionMismatch {
		cm.lastRemoteServerAttempt = time.Now()
		cm.remoteSimServerChan = TryConnectRemoteServer(cm.serverAddress, lg)
	}

	if cm.client != nil {
		cm.client.GetUpdates(es, p,
			func(err error) {
				es.Post(sim.Event{
					Type:        sim.StatusMessageEvent,
					WrittenText: "Error getting update from server: " + err.Error(),
				})
				if err == server.ErrRPCTimeout || util.IsRPCServerError(err) {
					cm.RemoteServer = nil
					if cm.client != nil {
						cm.client.Disconnect()
						cm.client = nil
					}
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
