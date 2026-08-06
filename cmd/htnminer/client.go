package main

import (
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/infrastructure/network/rpcclient"
	"github.com/pkg/errors"
)

const minerTimeout = 60 * time.Second

type minerClient struct {
	*rpcclient.RPCClient

	cfg                              *configFlags
	newBlockTemplateNotificationChan chan struct{}
}

func (mc *minerClient) connect() error {
	rpcAddress, err := mc.cfg.NetParams().NormalizeRPCServerAddress(mc.cfg.RPCServer)
	if err != nil {
		return err
	}
	rpcClient, err := rpcclient.NewRPCClient(rpcAddress)
	if err != nil {
		return err
	}
	mc.RPCClient = rpcClient
	mc.SetTimeout(minerTimeout)
	mc.SetLogger(backendLog, logger.LevelTrace)

	err = mc.RegisterForNewBlockTemplateNotifications(func(_ *appmessage.NewBlockTemplateNotificationMessage) {
		select {
		case mc.newBlockTemplateNotificationChan <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return errors.Wrapf(err, "error requesting new-block-template notifications")
	}

	log.Debugf("Connected to %s", rpcAddress)

	return nil
}

func newMinerClient(cfg *configFlags) (*minerClient, error) {
	minerClient := &minerClient{
		cfg:                              cfg,
		newBlockTemplateNotificationChan: make(chan struct{}),
	}

	err := minerClient.connect()
	if err != nil {
		return nil, err
	}

	return minerClient, nil
}
