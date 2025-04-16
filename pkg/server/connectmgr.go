// pkg/server/connectmgr.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"
	"log/slog"
	"net/rpc"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type ConnectionManager struct {
	localServerChan         chan *Server
	lastRemoteServerAttempt time.Time
	remoteSimServerChan     chan *serverConnection

	newSimConnectionChan     chan Connection
	serverRPCVersionMismatch bool

	lastRemoteSimsUpdate  time.Time
	updateRemoteSimsCall  *util.PendingCall
	updateRemoteSimsError error

	LocalServer   *Server
	RemoteServer  *Server
	serverAddress string

	client              *ControlClient
	connectionStartTime time.Time

	onNewClient func(*ControlClient)
	onError     func(error)
}

type Connection struct {
	SimState sim.State
	SimProxy *proxy
}

func MakeServerConnection(address, additionalScenario, additionalVideoMap string, e *util.ErrorLogger, lg *log.Logger,
	onNewClient func(*ControlClient), onError func(error)) (*ConnectionManager, error) {
	cm := &ConnectionManager{
		serverAddress:           address,
		lastRemoteServerAttempt: time.Now(),
		remoteSimServerChan:     TryConnectRemoteServer(address, lg),
		newSimConnectionChan:    make(chan Connection, 2),
		onNewClient:             onNewClient,
		onError:                 onError,
	}

	var err error
	cm.localServerChan, err = LaunchLocalServer(additionalScenario, additionalVideoMap, e, lg)
	return cm, err
}

func (cm *ConnectionManager) NewConnection(state sim.State, controllerToken string, client *util.RPCClient) {
	cm.newSimConnectionChan <- Connection{
		SimState: state,
		SimProxy: &proxy{
			ControllerToken: controllerToken,
			Client:          client,
		},
	}
}

func (cm *ConnectionManager) LoadLocalSim(s *sim.Sim, lg *log.Logger) (*ControlClient, error) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	var result NewSimResult
	if err := cm.LocalServer.Call("SimManager.AddLocal", s, &result); err != nil {
		return nil, err
	}

	cm.client = NewControlClient(*result.SimState, true, result.ControllerToken, cm.LocalServer.RPCClient, lg)
	cm.connectionStartTime = time.Now()

	return cm.client, nil
}

func (cm *ConnectionManager) CreateNewSim(config NewSimConfiguration, srv *Server) error {
	var result NewSimResult
	if err := srv.CallWithTimeout("SimManager.New", config, &result); err != nil {
		err = TryDecodeError(err)
		if err == ErrRPCTimeout || err == ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			cm.RemoteServer = nil
		}
		return err
	} else {
		cm.NewConnection(*result.SimState, result.ControllerToken, srv.RPCClient)
	}

	return nil
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
	if cm.updateRemoteSimsCall != nil && cm.updateRemoteSimsCall.CheckFinished() {
		cm.updateRemoteSimsCall = nil
		err := cm.updateRemoteSimsError
		cm.updateRemoteSimsError = nil
		return err
	} else if time.Since(cm.lastRemoteSimsUpdate) > 2*time.Second && cm.RemoteServer != nil {
		cm.lastRemoteSimsUpdate = time.Now()
		var rs map[string]*RemoteSim
		cm.updateRemoteSimsError = nil
		cm.updateRemoteSimsCall = &util.PendingCall{
			Call:      cm.RemoteServer.Go("SimManager.GetRunningSims", 0, &rs, nil),
			IssueTime: time.Now(),
			OnSuccess: func(result any) {
				if cm.RemoteServer != nil {
					cm.RemoteServer.setRunningSims(rs)
				}
			},
			OnErr: func(e error) {
				cm.updateRemoteSimsError = e

				// nil out the server if we've lost the connection; the
				// main loop will attempt to reconnect.
				if util.IsRPCServerError(e) {
					cm.RemoteServer = nil
				}
			},
		}
	}
	return nil
}

func (cm *ConnectionManager) Update(es *sim.EventStream, lg *log.Logger) {
	if cm.LocalServer == nil {
		cm.LocalServer = <-cm.localServerChan
	}

	select {
	case ns := <-cm.newSimConnectionChan:
		if cm.client != nil {
			cm.client.Disconnect()
		}
		cm.client = NewControlClient(ns.SimState, false, ns.SimProxy.ControllerToken, ns.SimProxy.Client, lg)
		cm.connectionStartTime = time.Now()

		if cm.onNewClient != nil {
			cm.onNewClient(cm.client)
		}

	case remoteServerConn := <-cm.remoteSimServerChan:
		if err := remoteServerConn.Err; err != nil {
			lg.Info("Unable to connect to remote server", slog.Any("error", err))

			if err.Error() == ErrRPCVersionMismatch.Error() {
				cm.serverRPCVersionMismatch = true
				if cm.onError != nil {
					cm.onError(ErrRPCVersionMismatch)
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
		cm.client.GetUpdates(es,
			func(err error) {
				es.Post(sim.Event{
					Type:    sim.StatusMessageEvent,
					Message: "Error getting update from server: " + err.Error(),
				})
				if err == ErrRPCTimeout || util.IsRPCServerError(err) {
					cm.RemoteServer = nil
					cm.client = nil
					if cm.onNewClient != nil {
						cm.onNewClient(nil)
					}
					if cm.onError != nil {
						cm.onError(ErrServerDisconnected)
					}
				} else if cm.onError != nil {
					cm.onError(err)
				}
			})
	}
}
