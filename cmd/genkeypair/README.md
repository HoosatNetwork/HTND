genkeypair
========

A tool for generating private-key-address pairs.

Note: This tool prints unencrypted private keys and is not recommended for day
to day use, and is intended mainly for tests.

In order to manage your funds it's recommended to use [htnwallet](../htnwallet)

To use a key generated with this tool in htnwallet, create a wallet of it with
`htnwallet import-private-key -f <keys file>` and run the wallet daemon on that
keys file. It is a wallet of its own, separate from any htnwallet wallet.
