package main

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/server/grpcserver/protowire"
)

var commandTypes = []reflect.Type{
	reflect.TypeFor[protowire.HoosatdMessage_AddPeerRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetConnectedPeerInfoRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetPeerAddressesRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetCurrentNetworkRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetInfoRequest](),

	reflect.TypeFor[protowire.HoosatdMessage_GetBlockRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetBlockByTransactionIdRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetBlocksRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetHeadersRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetBlockCountRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetBlockDagInfoRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetSelectedTipHashRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetVirtualSelectedParentBlueScoreRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetVirtualSelectedParentChainFromBlockRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_ResolveFinalityConflictRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_EstimateNetworkHashesPerSecondRequest](),

	reflect.TypeFor[protowire.HoosatdMessage_GetBlockTemplateRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_SubmitBlockRequest](),

	reflect.TypeFor[protowire.HoosatdMessage_GetMempoolEntryRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetMempoolEntriesRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetMempoolEntriesByAddressesRequest](),

	reflect.TypeFor[protowire.HoosatdMessage_SubmitTransactionRequest](),

	reflect.TypeFor[protowire.HoosatdMessage_GetUtxosByAddressesRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetBalanceByAddressRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_GetCoinSupplyRequest](),

	reflect.TypeFor[protowire.HoosatdMessage_BanRequest](),
	reflect.TypeFor[protowire.HoosatdMessage_UnbanRequest](),
}

type commandDescription struct {
	name       string
	parameters []*parameterDescription
	typeof     reflect.Type
}

type parameterDescription struct {
	name   string
	typeof reflect.Type
}

func commandDescriptions() []*commandDescription {
	commandDescriptions := make([]*commandDescription, len(commandTypes))

	for i, commandTypeWrapped := range commandTypes {
		commandType := unwrapCommandType(commandTypeWrapped)

		name := strings.TrimSuffix(commandType.Name(), "RequestMessage")
		numFields := commandType.NumField()

		var parameters []*parameterDescription
		for i := range numFields {
			field := commandType.Field(i)

			if !isFieldExported(field) {
				continue
			}

			parameters = append(parameters, &parameterDescription{
				name:   field.Name,
				typeof: field.Type,
			})
		}
		commandDescriptions[i] = &commandDescription{
			name:       name,
			parameters: parameters,
			typeof:     commandTypeWrapped,
		}
	}

	return commandDescriptions
}

func (cd *commandDescription) help() string {
	sb := &strings.Builder{}
	sb.WriteString(cd.name)
	for _, parameter := range cd.parameters {
		_, _ = fmt.Fprintf(sb, " [%s]", parameter.name)
	}
	return sb.String()
}
