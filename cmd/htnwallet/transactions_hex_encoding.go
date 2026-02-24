package main

import (
	"encoding/hex"
	"strings"
)

// hexTransactionsSeparator is used to mark the end of one transaction and the beginning of the next one.
// We use a separator that is not in the hex alphabet, but which will not split selection with a double click
const hexTransactionsSeparator = "_"

func encodeTransactionsToHex(transactions [][]byte) string {
	totalHexLen := 0
	for _, tx := range transactions {
		totalHexLen += hex.EncodedLen(len(tx))
	}
	totalHexLen += len(transactions) - 1 // separators, assume 1-byte sep

	buf := make([]byte, totalHexLen) // one alloc for everything
	offset := 0

	for i, tx := range transactions {
		if i > 0 {
			buf[offset] = hexTransactionsSeparator[0]
			offset++
		}
		n := hex.Encode(buf[offset:], tx)
		offset += n
	}
	return string(buf)
}

func decodeTransactionsFromHex(transactionsHex string) ([][]byte, error) {
	splitTransactionsHexes := strings.Split(transactionsHex, hexTransactionsSeparator)
	transactions := make([][]byte, len(splitTransactionsHexes))

	var err error
	for i, transactionHex := range splitTransactionsHexes {
		transactions[i], err = hex.DecodeString(transactionHex)
		if err != nil {
			return nil, err
		}
	}

	return transactions, nil
}
