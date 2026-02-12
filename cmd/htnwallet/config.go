package main

import (
	"os"

	"github.com/Hoosat-Oy/HTND/infrastructure/config"
	"github.com/pkg/errors"

	"github.com/jessevdk/go-flags"
)

const (
	createSubCmd                    = "create"
	balanceSubCmd                   = "balance"
	sendSubCmd                      = "send"
	autoCompoundSubCmd              = "auto-compound"
	voteSubCmd                      = "vote"
	sweepSubCmd                     = "sweep"
	createUnsignedTransactionSubCmd = "create-unsigned-transaction"
	signSubCmd                      = "sign"
	broadcastSubCmd                 = "broadcast"
	parseSubCmd                     = "parse"
	showAddressesSubCmd             = "show-addresses"
	newAddressSubCmd                = "new-address"
	dumpUnencryptedDataSubCmd       = "dump-unencrypted-data"
	startDaemonSubCmd               = "start-daemon"
	versionSubCmd                   = "version"
	getDaemonVersionSubCmd          = "get-daemon-version"
)

const (
	defaultListen    = "localhost:4282"
	defaultRPCServer = "localhost"
)

type configFlags struct {
	ShowVersion bool `short:"V" long:"version" description:"Display version information and exit"`
	config.NetworkFlags
}

type createConfig struct {
	KeysFile          string `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password          string `long:"password" short:"p" description:"Wallet password"`
	Yes               bool   `long:"yes" short:"y" description:"Assume \"yes\" to all questions"`
	MinimumSignatures uint32 `long:"min-signatures" short:"m" description:"Minimum required signatures" default:"1"`
	NumPrivateKeys    uint32 `long:"num-private-keys" short:"k" description:"Number of private keys" default:"1"`
	NumPublicKeys     uint32 `long:"num-public-keys" short:"n" description:"Total number of keys" default:"1"`
	ECDSA             bool   `long:"ecdsa" description:"Create an ECDSA wallet"`
	Import            bool   `long:"import" short:"i" description:"Import private keys (as opposed to generating them)"`
	config.NetworkFlags
}

type balanceConfig struct {
	DaemonAddress string `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	Verbose       bool   `long:"verbose" short:"v" description:"Verbose: show addresses with balance"`
	config.NetworkFlags
}

type sendConfig struct {
	KeysFile                 string   `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password                 string   `long:"password" short:"p" description:"Wallet password"`
	DaemonAddress            string   `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	ToAddress                string   `long:"to-address" short:"t" description:"The public address to send Hoosat to" required:"true"`
	FromAddresses            []string `long:"from-address" short:"a" description:"Specific public address to send Hoosat from. Repeat multiple times (adding -a before each) to accept several addresses" required:"false"`
	SendAmount               string   `long:"send-amount" short:"v" description:"An amount to send in Hoosat (e.g. 1234.12345678)"`
	IsSendAll                bool     `long:"send-all" description:"Send all the Hoosat in the wallet (mutually exclusive with --send-amount). If --from-address was used, will send all only from the specified addresses."`
	UseExistingChangeAddress bool     `long:"use-existing-change-address" short:"u" description:"Will use an existing change address (in case no change address was ever used, it will use a new one)"`
	Verbose                  bool     `long:"show-serialized" short:"s" description:"Show a list of hex encoded sent transactions"`
	config.NetworkFlags
}

type autoCompoundConfig struct {
	KeysFile                 string   `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password                 string   `long:"password" short:"p" description:"Wallet password"`
	CompoundRate             int      `long:"compound-rate" short:"c" description:"Time in seconds"`
	DaemonAddress            string   `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	ToAddress                string   `long:"to-address" short:"t" description:"The public address to compound your HTN to" required:"true"`
	FromAddresses            []string `long:"from-address" short:"a" description:"Specific public address to send Hoosat from. Repeat multiple times (adding -a before each) to accept several addresses" required:"false"`
	UseExistingChangeAddress bool     `long:"use-existing-change-address" short:"u" description:"Will use an existing change address (in case no change address was ever used, it will use a new one)"`
	Verbose                  bool     `long:"show-serialized" short:"s" description:"Show a list of hex encoded sent transactions"`
	config.NetworkFlags
}

type voteConfig struct {
	KeysFile                 string   `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password                 string   `long:"password" short:"p" description:"Wallet password"`
	DaemonAddress            string   `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	PollId                   string   `long:"poll-id" short:"i" description:"The poll ID to vote on" required:"true"`
	Votes                    []int    `long:"vote" short:"v" description:"Vote value(s). Repeat multiple times for multiple votes" required:"true"`
	FromAddresses            []string `long:"from-address" short:"a" description:"Specific public address to send Hoosat from. Repeat multiple times (adding -a before each) to accept several addresses" required:"false"`
	UseExistingChangeAddress bool     `long:"use-existing-change-address" short:"u" description:"Will use an existing change address (in case no change address was ever used, it will use a new one)"`
	Verbose                  bool     `long:"show-serialized" short:"s" description:"Show a list of hex encoded sent transactions"`
	config.NetworkFlags
}

type sweepConfig struct {
	PrivateKey    string `long:"private-key" short:"k" description:"Private key in hex format"`
	DaemonAddress string `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	config.NetworkFlags
}

type createUnsignedTransactionConfig struct {
	DaemonAddress            string   `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	ToAddress                string   `long:"to-address" short:"t" description:"The public address to send Hoosat to" required:"true"`
	FromAddresses            []string `long:"from-address" short:"a" description:"Specific public address to send Hoosat from. Use multiple times to accept several addresses" required:"false"`
	SendAmount               string   `long:"send-amount" short:"v" description:"An amount to send in Hoosat (e.g. 1234.12345678)"`
	IsSendAll                bool     `long:"send-all" description:"Send all the Hoosat in the wallet (mutually exclusive with --send-amount)"`
	UseExistingChangeAddress bool     `long:"use-existing-change-address" short:"u" description:"Will use an existing change address (in case no change address was ever used, it will use a new one)"`
	config.NetworkFlags
}

type signConfig struct {
	KeysFile        string `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password        string `long:"password" short:"p" description:"Wallet password"`
	Transaction     string `long:"transaction" short:"t" description:"The unsigned transaction(s) to sign on (encoded in hex)"`
	TransactionFile string `long:"transaction-file" short:"F" description:"The file containing the unsigned transaction(s) to sign on (encoded in hex)"`
	config.NetworkFlags
}

type broadcastConfig struct {
	DaemonAddress    string `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	Transactions     string `long:"transaction" short:"t" description:"The signed transaction to broadcast (encoded in hex)"`
	TransactionsFile string `long:"transaction-file" short:"F" description:"The file containing the unsigned transaction to sign on (encoded in hex)"`
	config.NetworkFlags
}

type parseConfig struct {
	Transaction     string `long:"transaction" short:"t" description:"The transaction to parse (encoded in hex)"`
	TransactionFile string `long:"transaction-file" short:"F" description:"The file containing the transaction to parse (encoded in hex)"`
	Verbose         bool   `long:"verbose" short:"v" description:"Verbose: show transaction inputs"`
	config.NetworkFlags
}

type showAddressesConfig struct {
	DaemonAddress string `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	config.NetworkFlags
}

type newAddressConfig struct {
	DaemonAddress string `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
	config.NetworkFlags
}

type startDaemonConfig struct {
	KeysFile  string `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password  string `long:"password" short:"p" description:"Wallet password"`
	RPCServer string `long:"rpcserver" short:"s" description:"RPC server to connect to"`
	Listen    string `long:"listen" short:"l" description:"Address to listen on (default: 0.0.0.0:8082)"`
	Timeout   uint32 `long:"wait-timeout" short:"w" description:"Waiting timeout for RPC calls, seconds (default: 30 s)"`
	Profile   string `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65536"`
	config.NetworkFlags
}

type dumpUnencryptedDataConfig struct {
	KeysFile string `long:"keys-file" short:"f" description:"Keys file location (default: ~/.htnwallet/keys.json (*nix), %USERPROFILE%\\AppData\\Local\\Hoosatwallet\\key.json (Windows))"`
	Password string `long:"password" short:"p" description:"Wallet password"`
	Yes      bool   `long:"yes" short:"y" description:"Assume \"yes\" to all questions"`
	config.NetworkFlags
}

type versionConfig struct {
}

type getDaemonVersionConfig struct {
	DaemonAddress string `long:"daemonaddress" short:"d" description:"Wallet daemon server to connect to"`
}

func parseCommandLine() (subCommand string, config interface{}) {
	cfg := &configFlags{}
	parser := flags.NewParser(cfg, flags.PrintErrors|flags.HelpFlag)

	createConf := &createConfig{}
	_, _ = parser.AddCommand(createSubCmd, "Creates a new wallet (`--import` to recover from seed)",
		"Creates a private key and 3 public addresses, one for each of MainNet, TestNet and DevNet. "+
			"Import existing private key and public addresses from seed using `--import`.", createConf)

	balanceConf := &balanceConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(balanceSubCmd, "Shows the balance of a public address",
		"Shows the balance for a public address in Hoosat", balanceConf)

	spamConf := &autoCompoundConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(autoCompoundSubCmd, "Sends a Hoosat compound transactions automatically",
		"Sends a Hoosat compound transactions automatically", spamConf)

	sendConf := &sendConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(sendSubCmd, "Sends a Hoosat transaction to a public address",
		"Sends a Hoosat transaction to a public address", sendConf)

	voteConf := &voteConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(voteSubCmd, "Votes on a poll by sending HTN with vote payload",
		"Votes on a poll by sending HTN with vote payload to the voting platform", voteConf)

	sweepConf := &sweepConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(sweepSubCmd, "Sends all funds associated with the given schnorr private key to a new address of the current wallet",
		"Sends all funds associated with the given schnorr private key to a newly created external (i.e. not a change) address of the "+
			"keyfile that is under the daemon's contol. Can be used with a private key generated with the genkeypair utilily "+
			"to send funds to your main wallet.", sweepConf)

	createUnsignedTransactionConf := &createUnsignedTransactionConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(createUnsignedTransactionSubCmd, "Create an unsigned Hoosat transaction",
		"Create an unsigned Hoosat transaction", createUnsignedTransactionConf)

	signConf := &signConfig{}
	_, _ = parser.AddCommand(signSubCmd, "Sign the given partially signed transaction",
		"Sign the given partially signed transaction", signConf)

	broadcastConf := &broadcastConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(broadcastSubCmd, "Broadcast the given transaction",
		"Broadcast the given transaction", broadcastConf)

	parseConf := &parseConfig{}
	_, _ = parser.AddCommand(parseSubCmd, "Parse the given transaction and print its contents",
		"Parse the given transaction and print its contents", parseConf)

	showAddressesConf := &showAddressesConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(showAddressesSubCmd, "Shows all generated public addresses of the current wallet",
		"Shows all generated public addresses of the current wallet", showAddressesConf)

	newAddressConf := &newAddressConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(newAddressSubCmd, "Generates new public address of the current wallet and shows it",
		"Generates new public address of the current wallet and shows it", newAddressConf)

	dumpUnencryptedDataConf := &dumpUnencryptedDataConfig{}
	_, _ = parser.AddCommand(dumpUnencryptedDataSubCmd, "Prints the unencrypted wallet data",
		"Prints the unencrypted wallet data including its private keys. Anyone that sees it can access "+
			"the funds. Use only on safe environment.", dumpUnencryptedDataConf)

	startDaemonConf := &startDaemonConfig{
		RPCServer: defaultRPCServer,
		Listen:    defaultListen,
	}
	_, _ = parser.AddCommand(startDaemonSubCmd, "Start the wallet daemon", "Start the wallet daemon", startDaemonConf)
	_, _ = parser.AddCommand(versionSubCmd, "Get the wallet version", "Get the wallet version", &versionConfig{})
	getDaemonVersionConf := &getDaemonVersionConfig{DaemonAddress: defaultListen}
	_, _ = parser.AddCommand(getDaemonVersionSubCmd, "Get the wallet daemon version", "Get the wallet daemon version", getDaemonVersionConf)

	_, err := parser.Parse()
	if err != nil {
		var flagsErr *flags.Error
		if ok := errors.As(err, &flagsErr); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			os.Exit(1)
		}
		return "", nil
	}

	switch parser.Command.Active.Name {
	case createSubCmd:
		combineNetworkFlags(&createConf.NetworkFlags, &cfg.NetworkFlags)
		err := createConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = createConf
	case balanceSubCmd:
		combineNetworkFlags(&balanceConf.NetworkFlags, &cfg.NetworkFlags)
		err := balanceConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = balanceConf
	case autoCompoundSubCmd:
		combineNetworkFlags(&spamConf.NetworkFlags, &cfg.NetworkFlags)
		err := spamConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = spamConf
	case sendSubCmd:
		combineNetworkFlags(&sendConf.NetworkFlags, &cfg.NetworkFlags)
		err := sendConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		err = validateSendConfig(sendConf)
		if err != nil {
			printErrorAndExit(err)
		}
		config = sendConf
	case voteSubCmd:
		combineNetworkFlags(&voteConf.NetworkFlags, &cfg.NetworkFlags)
		err := voteConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = voteConf
	case sweepSubCmd:
		combineNetworkFlags(&sweepConf.NetworkFlags, &cfg.NetworkFlags)
		err := sweepConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = sweepConf
	case createUnsignedTransactionSubCmd:
		combineNetworkFlags(&createUnsignedTransactionConf.NetworkFlags, &cfg.NetworkFlags)
		err := createUnsignedTransactionConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		err = validateCreateUnsignedTransactionConf(createUnsignedTransactionConf)
		if err != nil {
			printErrorAndExit(err)
		}
		config = createUnsignedTransactionConf
	case signSubCmd:
		combineNetworkFlags(&signConf.NetworkFlags, &cfg.NetworkFlags)
		err := signConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = signConf
	case broadcastSubCmd:
		combineNetworkFlags(&broadcastConf.NetworkFlags, &cfg.NetworkFlags)
		err := broadcastConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = broadcastConf
	case parseSubCmd:
		combineNetworkFlags(&parseConf.NetworkFlags, &cfg.NetworkFlags)
		err := parseConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = parseConf
	case showAddressesSubCmd:
		combineNetworkFlags(&showAddressesConf.NetworkFlags, &cfg.NetworkFlags)
		err := showAddressesConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = showAddressesConf
	case newAddressSubCmd:
		combineNetworkFlags(&newAddressConf.NetworkFlags, &cfg.NetworkFlags)
		err := newAddressConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = newAddressConf
	case dumpUnencryptedDataSubCmd:
		combineNetworkFlags(&dumpUnencryptedDataConf.NetworkFlags, &cfg.NetworkFlags)
		err := dumpUnencryptedDataConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = dumpUnencryptedDataConf
	case startDaemonSubCmd:
		combineNetworkFlags(&startDaemonConf.NetworkFlags, &cfg.NetworkFlags)
		err := startDaemonConf.ResolveNetwork(parser)
		if err != nil {
			printErrorAndExit(err)
		}
		config = startDaemonConf
	case versionSubCmd:
	case getDaemonVersionSubCmd:
		config = getDaemonVersionConf
	}

	return parser.Command.Active.Name, config
}

func validateCreateUnsignedTransactionConf(conf *createUnsignedTransactionConfig) error {
	if (!conf.IsSendAll && conf.SendAmount == "") ||
		(conf.IsSendAll && conf.SendAmount != "") {

		return errors.New("exactly one of '--send-amount' or '--send-all' must be specified")
	}
	return nil
}

func validateSendConfig(conf *sendConfig) error {
	if (!conf.IsSendAll && conf.SendAmount == "") ||
		(conf.IsSendAll && conf.SendAmount != "") {

		return errors.New("exactly one of '--send-amount' or '--send-all' must be specified")
	}
	return nil
}

func combineNetworkFlags(dst, src *config.NetworkFlags) {
	dst.Testnet = dst.Testnet || src.Testnet
	dst.Simnet = dst.Simnet || src.Simnet
	dst.Devnet = dst.Devnet || src.Devnet
	if dst.OverrideDAGParamsFile == "" {
		dst.OverrideDAGParamsFile = src.OverrideDAGParamsFile
	}
}
