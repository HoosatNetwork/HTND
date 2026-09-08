package mempool

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func outpointsForTest(count int) []*externalapi.DomainOutpoint {
	outpoints := make([]*externalapi.DomainOutpoint, 0, count)
	for i := 0; i < count; i++ {
		id := externalapi.NewDomainTransactionIDFromByteArray(
			&[externalapi.DomainHashSize]byte{byte(i + 1)})
		outpoints = append(outpoints, &externalapi.DomainOutpoint{
			TransactionID: *id,
			Index:         uint32(i),
		})
	}
	return outpoints
}

// TestFormatOutpointsStaysReadable pins that naming the missing inputs of a dropped transaction
// does not turn one log line into a screenful. A compounding transaction can carry a hundred
// inputs, and the point of the line is to identify the transaction and give enough coins to trace,
// not to dump all of them.
func TestFormatOutpointsStaysReadable(t *testing.T) {
	if formatted := formatOutpoints(outpointsForTest(2)); strings.Contains(formatted, "more") {
		t.Errorf("a short list should be shown in full, got: %s", formatted)
	}

	formatted := formatOutpoints(outpointsForTest(100))
	if !strings.Contains(formatted, "and 96 more") {
		t.Errorf("expected a long list to be truncated with a count, got: %s", formatted)
	}
	if strings.Count(formatted, ":") != 4 {
		t.Errorf("expected exactly 4 outpoints to be listed, got: %s", formatted)
	}
}

func TestFormatOutpointsHandlesEmpty(t *testing.T) {
	if formatted := formatOutpoints(nil); formatted != "" {
		t.Errorf("expected an empty string for no outpoints, got: %q", formatted)
	}
}
