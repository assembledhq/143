package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxHostID(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, input, want string }{
		{"canonical IPv4", " 192.0.2.1 ", "192.0.2.1"},
		{"canonical IPv6", "2001:db8:0:0::1", "2001:db8::1"},
		{"private IPv4", "10.0.0.4", "10.0.0.4"},
		{"unknown identity", "", ""},
		{"unspecified", "0.0.0.0", ""},
		{"loopback", "127.0.0.1", ""},
		{"multicast", "ff02::1", ""},
		{"link-local needs scope", "fe80::1", ""},
		{"container hostname is not identity", "container-generation", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, (Config{WorkerPrivateIP: tt.input}).SandboxHostID(), "only provisioned IP identity may supply a physical sandbox budget")
		})
	}
}
