package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jessevdk/go-flags"
)

func TestRPCAllowIPConfigParsing(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantLen int
		wantErr bool
	}{
		{name: "IPv4 address and CIDR", value: "192.0.2.1, 198.51.100.0/24", wantLen: 2},
		{name: "IPv6 address and CIDR", value: "2001:db8::1, 2001:db8:1::/48", wantLen: 2},
		{name: "empty entry", value: "192.0.2.1,,198.51.100.0/24", wantErr: true},
		{name: "trailing comma", value: "192.0.2.1,", wantErr: true},
		{name: "invalid entry", value: "not-an-ip", wantErr: true},
		{name: "empty value", value: "", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			networks, err := parseRPCAllowIPNetworks(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseRPCAllowIPNetworks(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
			if !test.wantErr && len(networks) != test.wantLen {
				t.Fatalf("parsed %d networks, want %d", len(networks), test.wantLen)
			}
			if !test.wantErr && test.name == "IPv4 address and CIDR" && networks[0].String() != "192.0.2.1/32" {
				t.Errorf("bare IPv4 address became %s, want /32", networks[0])
			}
			if !test.wantErr && test.name == "IPv6 address and CIDR" && networks[0].String() != "2001:db8::1/128" {
				t.Errorf("bare IPv6 address became %s, want /128", networks[0])
			}
		})
	}
}

func TestRPCAllowIPFlagAndINIParsing(t *testing.T) {
	t.Run("command line", func(t *testing.T) {
		cfgFlags := defaultFlags()
		parser := newConfigParser(cfgFlags, flags.None)
		if _, err := parser.ParseArgs([]string{"--rpcallowip=192.0.2.0/24,198.51.100.7"}); err != nil {
			t.Fatalf("ParseArgs: %v", err)
		}
		if !cfgFlags.RPCAllowIPs.Set || cfgFlags.RPCAllowIPs.Value != "192.0.2.0/24,198.51.100.7" {
			t.Fatalf("RPCAllowIPs = %#v, want explicitly set input", cfgFlags.RPCAllowIPs)
		}
	})

	t.Run("ini file", func(t *testing.T) {
		cfgFlags := defaultFlags()
		parser := newConfigParser(cfgFlags, flags.None)
		path := filepath.Join(t.TempDir(), "htnd.conf")
		if err := os.WriteFile(path, []byte("[Application Options]\nrpcallowip=192.0.2.0/24, 198.51.100.7\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := flags.NewIniParser(parser).ParseFile(path); err != nil {
			t.Fatalf("ParseFile: %v", err)
		}
		if !cfgFlags.RPCAllowIPs.Set || cfgFlags.RPCAllowIPs.Value != "192.0.2.0/24, 198.51.100.7" {
			t.Fatalf("RPCAllowIPs = %#v, want explicitly set INI value", cfgFlags.RPCAllowIPs)
		}
	})

	t.Run("explicit empty command line value", func(t *testing.T) {
		cfgFlags := defaultFlags()
		parser := newConfigParser(cfgFlags, flags.None)
		if _, err := parser.ParseArgs([]string{"--rpcallowip="}); err != nil {
			t.Fatalf("ParseArgs: %v", err)
		}
		if !cfgFlags.RPCAllowIPs.Set {
			t.Fatal("explicit empty value was not recorded as set")
		}
		if _, err := parseRPCAllowIPNetworks(cfgFlags.RPCAllowIPs.Value); err == nil {
			t.Fatal("explicit empty value was accepted")
		}
	})
}

func TestRPCAllowlistListenerAddresses(t *testing.T) {
	existing := []string{"127.0.0.1:42420"}
	got := rpcListenersWithAllowlist(existing, "42420", false)
	if got[0] != existing[0] || got[1] != "0.0.0.0:42420" {
		t.Fatalf("listeners = %v, want existing listener plus IPv4 wildcard", got)
	}

	got = rpcListenersWithAllowlist(nil, "42422", true)
	if len(got) != 2 || got[0] != "0.0.0.0:42422" || got[1] != "[::]:42422" {
		t.Fatalf("listeners = %v, want IPv4 and IPv6 wildcards on configured port", got)
	}

	got = rpcListenersWithAllowlist([]string{":42420"}, "42420", true)
	if len(got) != 2 || got[0] != "0.0.0.0:42420" || got[1] != "[::]:42420" {
		t.Fatalf("listeners = %v, want generic wildcard replaced with separate IPv4 and IPv6 wildcards", got)
	}

	got = rpcListenersWithAllowlist([]string{"[::]:42420"}, "42420", false)
	if len(got) != 2 || got[0] != "[::]:42420" || got[1] != "0.0.0.0:42420" {
		t.Fatalf("listeners = %v, want existing IPv6 wildcard and IPv4 wildcard without a conflicting loopback listener", got)
	}
}
