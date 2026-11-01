package protowire

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

func TestBlockHeaderTimestampValidation(t *testing.T) {
	zeroHash := &Hash{Bytes: make([]byte, externalapi.DomainHashSize)}
	tests := []struct {
		timestamp int64
		valid     bool
	}{
		{timestamp: 1760700000000, valid: true},
		{timestamp: 0, valid: true},
		{timestamp: -1, valid: false},
		{timestamp: 3464438340122739978, valid: false},
	}
	for _, test := range tests {
		header := &BlockHeader{
			Version:              1,
			HashMerkleRoot:       zeroHash,
			AcceptedIdMerkleRoot: zeroHash,
			UtxoCommitment:       zeroHash,
			PruningPoint:         zeroHash,
			Timestamp:            test.timestamp,
		}
		_, err := header.toAppMessage()
		if test.valid && err != nil {
			t.Errorf("P2P header timestamp %d: unexpected error: %s", test.timestamp, err)
		}
		if !test.valid && err == nil {
			t.Errorf("P2P header timestamp %d: expected an error", test.timestamp)
		}

		zero := externalapi.DomainHash{}
		rpcBlock := &appmessage.RPCBlock{Header: &appmessage.RPCBlockHeader{
			Version:              1,
			HashMerkleRoot:       zero.String(),
			AcceptedIDMerkleRoot: zero.String(),
			UTXOCommitment:       zero.String(),
			PruningPoint:         zero.String(),
			BlueWork:             "0",
			Timestamp:            test.timestamp,
		}}
		_, err = appmessage.RPCBlockToDomainBlock(rpcBlock, "x")
		if test.valid && err != nil {
			t.Errorf("RPC header timestamp %d: unexpected error: %s", test.timestamp, err)
		}
		if !test.valid && err == nil {
			t.Errorf("RPC header timestamp %d: expected an error", test.timestamp)
		}
	}
}
