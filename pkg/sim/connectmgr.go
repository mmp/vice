// pkg/sim/connectmgr.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/util"
)

type ConnectionManager struct {
	localServerChan         chan *Server
	lastRemoteServerAttempt time.Time
	remoteSimServerChan     chan *serverConnection

	newSimConnectionChan     chan Connection
	serverRPCVersionMismatch bool

	localServer   *Server
	remoteServer  *Server
	serverAddress string

	client              *ControlClient
	connectionStartTime time.Time

	onNewClient func(*ControlClient)
	onError     func(error)
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

func (cm *ConnectionManager) NewConnection(c Connection) {
	cm.newSimConnectionChan <- c
}

func (cm *ConnectionManager) LoadLocalSim(s *Sim, lg *log.Logger) (*ControlClient, error) {
	if cm.localServer == nil {
		cm.localServer = <-cm.localServerChan
	}

	var result NewSimResult
	if err := cm.localServer.Call("SimManager.Add", s, &result); err != nil {
		return nil, err
	}

	cm.client = NewControlClient(*result.SimState, result.ControllerToken, cm.localServer.RPCClient, lg)
	cm.connectionStartTime = time.Now()

	return cm.client, nil
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
	if cm.localServer == nil {
		cm.localServer = <-cm.localServerChan
	}

	return cm.client != nil && cm.client.RPCClient() == cm.localServer.RPCClient
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

func (cm *ConnectionManager) Update(es *EventStream, lg *log.Logger) {
	if cm.localServer == nil {
		cm.localServer = <-cm.localServerChan
	}

	select {
	case ns := <-cm.newSimConnectionChan:
		if cm.client != nil {
			cm.client.Disconnect()
		}
		cm.client = NewControlClient(ns.SimState, ns.SimProxy.ControllerToken,
			ns.SimProxy.Client, lg)
		cm.connectionStartTime = time.Now()

		if cm.onNewClient != nil {
			cm.onNewClient(cm.client)
		}

	case remoteServerConn := <-cm.remoteSimServerChan:
		if err := remoteServerConn.Err; err != nil {
			lg.Warn("Unable to connect to remote server", slog.Any("error", err))

			if err.Error() == ErrRPCVersionMismatch.Error() {
				cm.serverRPCVersionMismatch = true
				if cm.onError != nil {
					cm.onError(ErrRPCVersionMismatch)
				}
			}
			cm.remoteServer = nil
		} else {
			cm.remoteServer = remoteServerConn.Server
		}

	default:
	}

	if cm.remoteServer == nil && time.Since(cm.lastRemoteServerAttempt) > 10*time.Second && !cm.serverRPCVersionMismatch {
		cm.lastRemoteServerAttempt = time.Now()
		cm.remoteSimServerChan = TryConnectRemoteServer(cm.serverAddress, lg)
	}

	if cm.client != nil {
		cm.client.GetUpdates(es,
			func(err error) {
				es.Post(Event{
					Type:    StatusMessageEvent,
					Message: "Error getting update from server: " + err.Error(),
				})
				if err == ErrRPCTimeout || util.IsRPCServerError(err) {
					cm.remoteServer = nil
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
