package pruningmanager

import (
	"context"
	"errors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"testing"
)

type cancellationCursor struct {
	model.DBCursor
	closed bool
}

func (c *cancellationCursor) Close() error { c.closed = true; return nil }

func TestPruningReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	raw := &cancellationCursor{}
	cursor := &cancelableHistoryCursor{DBCursor: raw, ctx: ctx}
	cancel()
	if cursor.First() || cursor.Next() {
		t.Fatal("cancelled iterator advanced")
	}
	if _, err := cursor.Key(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := cursor.Value(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := cursor.Seek(nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := cursor.Close(); !errors.Is(err, context.Canceled) || !raw.closed {
		t.Fatalf("close %t: %v", raw.closed, err)
	}
	base := externalapi.NewDomainHashFromByteArray(&[32]byte{})
	target := externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	pm := &pruningManager{genesisHash: base}
	// A cancelled derivation cannot read even the first input from its snapshot.
	if _, err := pm.readViewWithContext(ctx, &database.ReadSnapshot{}, database.MakeBucket([]byte("instance")), 0, base, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("view admission: %v", err)
	}
}
