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
	"strings"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

type ConnectionManager struct {
	localServerChan         chan *Server
	lastRemoteServerAttempt time.Time
	remoteSimServerChan     chan *serverConnection

	serverRPCVersionMismatch bool

	lastRemoteSimsUpdate  time.Time
	updateRemoteSimsCall  *pendingCall
	updateRemoteSimsError error

	// METAR fetch state
	pendingMETARCall *pendingCall

	LocalServer   *Server
	RemoteServer  *Server
	serverAddress string

	client              *ControlClient
	connectionStartTime time.Time

	onNewClient func(*ControlClient)
	onError     func(error)
}

func MakeServerManager(serverAddress, additionalScenario, additionalVideoMap string, lg *log.Logger,
	onNewClient func(*ControlClient), onError func(error)) (*ConnectionManager, util.ErrorLogger, string) {
	cm := &ConnectionManager{
		serverAddress:           serverAddress,
		lastRemoteServerAttempt: time.Now(),
		remoteSimServerChan:     TryConnectRemoteServer(serverAddress, lg),
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
					RPCClient:   client,
					HaveTTS:     cr.HaveTTS,
					name:        "Local (Single controller)",
					configs:     cr.Configurations,
					runningSims: cr.RunningSims,
				}
			}
		}
	}

	return cm, errorLogger, extraScenarioErrors
}

func (cm *ConnectionManager) LoadLocalSim(s *sim.Sim, lg *log.Logger) (*ControlClient, error) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	var result server.NewSimResult
	if err := cm.LocalServer.Call(server.AddLocalRPC, s, &result); err != nil {
		return nil, err
	}

	wsAddress := "localhost:" + strconv.Itoa(result.SpeechWSPort)
	cm.client = NewControlClient(*result.SimState, result.ControllerToken, wsAddress, cm.LocalServer.RPCClient, lg)
	cm.connectionStartTime = time.Now()

	return cm.client, nil
}

func (cm *ConnectionManager) CreateNewSim(config server.NewSimConfiguration, srv *Server, lg *log.Logger) error {
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
		cm.handleSuccessfulConnection(result, srv, config.DisableTextToSpeech, lg)
		return nil
	}
}

// handleSuccessfulConnection handles the common logic for setting up a client
// connection after a successful RPC call to create or join a sim
func (cm *ConnectionManager) handleSuccessfulConnection(result server.NewSimResult, srv *Server,
	disableTextToSpeech bool, lg *log.Logger) {
	if cm.client != nil {
		cm.client.Disconnect()
	}

	var wsAddress string
	// Only set websocket address if TTS is not disabled
	if !disableTextToSpeech {
		if srv == cm.LocalServer {
			wsAddress = "localhost:" + strconv.Itoa(result.SpeechWSPort)
		} else {
			wsAddress, _, _ = strings.Cut(cm.serverAddress, ":")
			wsAddress += ":" + strconv.Itoa(result.SpeechWSPort)
		}
	}
	cm.client = NewControlClient(*result.SimState, result.ControllerToken, wsAddress, srv.RPCClient, lg)

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

func (cm *ConnectionManager) UpdateRemoteSims() error {
	if cm.updateRemoteSimsCall != nil && cm.updateRemoteSimsCall.CheckFinished(nil, nil) {
		cm.updateRemoteSimsCall = nil
		err := cm.updateRemoteSimsError
		cm.updateRemoteSimsError = nil
		return err
	} else if time.Since(cm.lastRemoteSimsUpdate) > 2*time.Second &&
		cm.RemoteServer != nil && cm.updateRemoteSimsCall == nil {
		cm.lastRemoteSimsUpdate = time.Now()

		var rs map[string]*server.RemoteSim
		cm.updateRemoteSimsError = nil
		cm.updateRemoteSimsCall = makeRPCCall(cm.RemoteServer.Go(server.GetRunningSimsRPC, 0, &rs, nil),
			func(err error) {
				if err == nil {
					if cm.RemoteServer != nil {
						cm.RemoteServer.setRunningSims(rs)
					}
				} else {
					cm.updateRemoteSimsError = err

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

func (cm *ConnectionManager) UpdateAvailableWX(srv *Server) error {
	if srv == nil {
		return nil
	}
	return srv.UpdateAvailableWX()
}

func (cm *ConnectionManager) ConnectToSim(config server.SimConnectionConfiguration, srv *Server, lg *log.Logger) error {
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
		cm.handleSuccessfulConnection(result, srv, config.DisableTextToSpeech, lg)
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
		}

	default:
	}

	if cm.RemoteServer == nil && time.Since(cm.lastRemoteServerAttempt) > 10*time.Second && !cm.serverRPCVersionMismatch {
		cm.lastRemoteServerAttempt = time.Now()
		cm.remoteSimServerChan = TryConnectRemoteServer(cm.serverAddress, lg)
	}

	if cm.pendingMETARCall != nil && cm.pendingMETARCall.CheckFinished(nil, nil) {
		cm.pendingMETARCall = nil
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

func (cm *ConnectionManager) GetMETAR(srv *Server, airports []string, callback func(map[string][]wx.METAR, error)) {
	if srv == nil || srv.RPCClient == nil {
		callback(nil, errors.New("no server available"))
		return
	}

	// Cancel any pending METAR call
	cm.pendingMETARCall = nil

	var soaMETAR map[string]wx.METARSOA
	cm.pendingMETARCall = makeRPCCall(srv.Go(server.GetMETARRPC, airports, &soaMETAR, nil),
		func(err error) {
			if err != nil {
				callback(nil, err)
			} else {
				m := make(map[string][]wx.METAR)
				for ap, soa := range soaMETAR {
					m[ap] = wx.DecodeMETARSOA(soa)
				}
				callback(m, nil)
			}
		})
}
