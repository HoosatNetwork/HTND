package main

import (
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
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

// requestTemplateRefresh asks the templates loop to fetch a new template as soon as it can.
func (mc *minerClient) requestTemplateRefresh() {
	select {
	case mc.newBlockTemplateNotificationChan <- struct{}{}:
	default:
	}
}

func newMinerClient(cfg *configFlags) (*minerClient, error) {
	minerClient := &minerClient{
		cfg: cfg,
		// Buffered so a notification arriving while the templates loop is inside an RPC call is kept
		// instead of dropped, which left the threads on a stale template until the next poll.
		newBlockTemplateNotificationChan: make(chan struct{}, 1),
	}

	err := minerClient.connect()
	if err != nil {
		return nil, err
	}

	return minerClient, nil
}
