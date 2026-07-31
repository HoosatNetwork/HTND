package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/stability-tests/common/rpc"
	"github.com/pkg/errors"
)

func checkedDurationFromCount(count uint64, unit time.Duration) (time.Duration, error) {
	parsedCount, err := strconv.ParseInt(strconv.FormatUint(count, 10), 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(parsedCount) * unit, nil
}

func checkResolveVirtual(syncerClient, syncedClient *rpc.Client) error {
	err := syncedClient.RegisterForBlockAddedNotifications()
	if err != nil {
		return errors.Wrap(err, "error registering for blockAdded notifications")
	}

	syncedBlockCountResponse, err := syncedClient.GetBlockCount()
	if err != nil {
		return err
	}

	rejectReason, err := mineOnTips(syncerClient)
	if err != nil {
		panic(err)
	}
	if rejectReason != appmessage.RejectReasonNone {
		panic(fmt.Sprintf("mined block rejected: %s", rejectReason))
	}

	expectedDuration, err := checkedDurationFromCount(syncedBlockCountResponse.BlockCount, 100*time.Millisecond)
	if err != nil {
		return err
	}
	start := time.Now()
	select {
	case <-time.After(expectedDuration):
		return errors.Errorf("it took more than %s to resolve the virtual", expectedDuration)
	case <-syncedClient.OnBlockAdded:
	}

	log.Infof("It took %s to resolve the virtual", time.Since(start))
	return nil
}
