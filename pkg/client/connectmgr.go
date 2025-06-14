// pkg/client/connectmgr.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"errors"
	"fmt"
	"log/slog"
	"net/rpc"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type ConnectionManager struct {
	localServerChan         chan *Server
	lastRemoteServerAttempt time.Time
	remoteSimServerChan     chan *serverConnection

	serverRPCVersionMismatch bool

	lastRemoteSimsUpdate  time.Time
	updateRemoteSimsCall  *pendingCall
	updateRemoteSimsError error

	LocalServer   *Server
	RemoteServer  *Server
	serverAddress string

	client              *ControlClient
	connectionStartTime time.Time

	onNewClient func(*ControlClient)
	onError     func(error)
}

func MakeServerManager(address, additionalScenario, additionalVideoMap string, lg *log.Logger,
	onNewClient func(*ControlClient), onError func(error)) (*ConnectionManager, util.ErrorLogger) {
	cm := &ConnectionManager{
		serverAddress:           address,
		lastRemoteServerAttempt: time.Now(),
		remoteSimServerChan:     TryConnectRemoteServer(address, lg),
		onNewClient:             onNewClient,
		onError:                 onError,
	}

	// Launch local server
	rpcPort, errorLogger := server.LaunchServerAsync(server.ServerLaunchConfig{
		ExtraScenario: additionalScenario,
		ExtraVideoMap: additionalVideoMap,
	}, lg)

	if !errorLogger.HaveErrors() {
		client, err := getClient(fmt.Sprintf("localhost:%d", rpcPort), lg)
		if err != nil {
			errorLogger.Error(err)
		} else {
			var cr server.ConnectResult
			if err := client.callWithTimeout("SimManager.Connect", server.ViceRPCVersion, &cr); err != nil {
				errorLogger.Error(err)
			} else {
				cm.LocalServer = &Server{
					RPCClient:   client,
					name:        "Local (Single controller)",
					configs:     cr.Configurations,
					runningSims: cr.RunningSims,
				}
			}
		}
	}

	return cm, errorLogger
}

func (cm *ConnectionManager) LoadLocalSim(s *sim.Sim, lg *log.Logger) (*ControlClient, error) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	var result server.NewSimResult
	if err := cm.LocalServer.Call("SimManager.AddLocal", s, &result); err != nil {
		return nil, err
	}

	wsAddress := "localhost:" + strconv.Itoa(result.SpeechWSPort)
	cm.client = NewControlClient(*result.SimState, true, result.ControllerToken, wsAddress, cm.LocalServer.RPCClient, lg)
	cm.connectionStartTime = time.Now()

	return cm.client, nil
}

func (cm *ConnectionManager) CreateNewSim(config server.NewSimConfiguration, srv *Server, lg *log.Logger) error {
	var result server.NewSimResult
	if err := srv.callWithTimeout("SimManager.New", config, &result); err != nil {
		err = server.TryDecodeError(err)
		if err == server.ErrRPCTimeout || err == server.ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			cm.RemoteServer = nil
		}
		return err
	} else {
		if cm.client != nil {
			cm.client.Disconnect()
		}

		var wsAddress string
		if srv == cm.LocalServer {
			wsAddress = "localhost:" + strconv.Itoa(result.SpeechWSPort)
		} else {
			wsAddress, _, _ = strings.Cut(cm.serverAddress, ":")
			wsAddress += ":" + strconv.Itoa(result.SpeechWSPort)
		}
		cm.client = NewControlClient(*result.SimState, false, result.ControllerToken, wsAddress, srv.RPCClient, lg)

		cm.connectionStartTime = time.Now()

		if cm.onNewClient != nil {
			cm.onNewClient(cm.client)
		}

		return nil
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
		cm.updateRemoteSimsCall = makeRPCCall(cm.RemoteServer.Go("SimManager.GetRunningSims", 0, &rs, nil),
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

	if cm.client != nil {
		cm.client.GetUpdates(es, p,
			func(err error) {
				es.Post(sim.Event{
					Type:        sim.StatusMessageEvent,
					WrittenText: "Error getting update from server: " + err.Error(),
				})
				if err == server.ErrRPCTimeout || util.IsRPCServerError(err) {
					cm.RemoteServer = nil
					cm.client = nil
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
