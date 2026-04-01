package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Hoosat-Oy/HTND/version"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/server/grpcserver/protowire"

	"github.com/pkg/errors"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/rpcclient/grpcclient"
)

func main() {
	cfg, err := parseConfig()
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error parsing command-line arguments: %s", err))
	}
	if cfg.ListCommands {
		printAllCommands()
		return
	}

	rpcAddress, err := cfg.NetParams().NormalizeRPCServerAddress(cfg.RPCServer)
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error parsing RPC server address: %s", err))
	}
	client, err := grpcclient.Connect(rpcAddress)
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error connecting to the RPC server: %s", err))
	}
	defer func() { _ = client.Disconnect() }()

	timeout := time.Duration(cfg.Timeout) * time.Second

	if !cfg.AllowConnectionToDifferentVersions {
		htndMessage, err := postWithTimeout(client,
			&protowire.HoosatdMessage{Payload: &protowire.HoosatdMessage_GetInfoRequest{GetInfoRequest: &protowire.GetInfoRequestMessage{}}},
			timeout)
		if err != nil {
			printErrorAndExit(fmt.Sprintf("Cannot post GetInfo message: %s", err))
		}

		localVersion := version.Version()
		remoteVersion := htndMessage.GetGetInfoResponse().ServerVersion

		if localVersion != remoteVersion {
			printErrorAndExit(fmt.Sprintf("Server version mismatch, expect: %s, got: %s", localVersion, remoteVersion))
		}
	}

	responseChan := make(chan string)

	if cfg.RequestJSON != "" {
		go postJSON(cfg, client, responseChan)
	} else {
		go postCommand(cfg, client, responseChan)
	}

	select {
	case responseString := <-responseChan:
		prettyResponseString := prettifyResponse(responseString)
		fmt.Println(prettyResponseString)
	case <-time.After(timeout):
		printErrorAndExit(fmt.Sprintf("timeout of %s has been exceeded", timeout))
	}
}

func postWithTimeout(client *grpcclient.GRPCClient, message *protowire.HoosatdMessage,
	timeout time.Duration,
) (*protowire.HoosatdMessage, error) {
	type result struct {
		message *protowire.HoosatdMessage
		err     error
	}

	resultChan := make(chan result, 1)
	go func() {
		response, err := client.Post(message)
		resultChan <- result{message: response, err: err}
	}()

	select {
	case res := <-resultChan:
		return res.message, res.err
	case <-time.After(timeout):
		return nil, errors.Errorf("timeout of %s has been exceeded", timeout)
	}
}

func printAllCommands() {
	requestDescs := commandDescriptions()
	for _, requestDesc := range requestDescs {
		fmt.Printf("\t%s\n", requestDesc.help())
	}
}

func postCommand(cfg *configFlags, client *grpcclient.GRPCClient, responseChan chan string) {
	message, err := parseCommand(cfg.CommandAndParameters, commandDescriptions())
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error parsing command: %s", err))
	}

	response, err := client.Post(message)
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error posting the request to the RPC server: %s", err))
	}
	responseBytes, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(response)
	if err != nil {
		printErrorAndExit(errors.Wrapf(err, "error parsing the response from the RPC server").Error())
	}

	responseChan <- string(responseBytes)
}

func postJSON(cfg *configFlags, client *grpcclient.GRPCClient, doneChan chan string) {
	responseString, err := client.PostJSON(cfg.RequestJSON)
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error posting the request to the RPC server: %s", err))
	}
	doneChan <- responseString
}

func prettifyResponse(response string) string {
	htndMessage := &protowire.HoosatdMessage{}
	err := protojson.Unmarshal([]byte(response), htndMessage)
	if err != nil {
		printErrorAndExit(fmt.Sprintf("error parsing the response from the RPC server: %s", err))
	}

	marshalOptions := &protojson.MarshalOptions{}
	marshalOptions.Indent = "    "
	marshalOptions.EmitUnpopulated = true
	return marshalOptions.Format(htndMessage)
}

func printErrorAndExit(message string) {
	fmt.Fprintf(os.Stderr, "%s\n", message)
	os.Exit(1)
}
