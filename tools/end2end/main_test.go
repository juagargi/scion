// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	hummlib "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
)

// TestHbirdSecretValuePerAS verifies secret values are derived per IA and cached.
func TestHbirdSecretValuePerAS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ia110 := addr.MustParseIA("1-ff00:0:110")
	ia111 := addr.MustParseIA("1-ff00:0:111")
	writeMasterKeyPair(t, dir, ia110, []byte("secret-key-as110-000"), []byte("unused"))
	writeMasterKeyPair(t, dir, ia111, []byte("secret-key-as111-111"), []byte("unused"))

	c := client{
		hummKeysDir: dir,
		hummSVByIA:  make(map[addr.IA][]byte),
	}

	sv110, err := c.hummSecretValue(ia110)
	require.NoError(t, err)
	requireManualDerivationMatchesClient(t, [2]string{dir, "ASff00_0_110"}, sv110)
	sv111, err := c.hummSecretValue(ia111)
	require.NoError(t, err)
	requireManualDerivationMatchesClient(t, [2]string{dir, "ASff00_0_111"}, sv111)
	require.NotEqual(t, sv110, sv111)

	againSV, err := c.hummSecretValue(ia110)
	require.NoError(t, err)
	require.Equal(t, sv110, againSV)

	againSV, err = c.hummSecretValue(ia111)
	require.NoError(t, err)
	require.Equal(t, sv111, againSV)
}

// TestHbirdSecretValueMissingKeys verifies missing key material returns an error.
func TestHbirdSecretValueMissingKeys(t *testing.T) {
	t.Parallel()
	c := client{
		hummKeysDir: t.TempDir(),
		hummSVByIA:  make(map[addr.IA][]byte),
	}
	_, err := c.hummSecretValue(addr.MustParseIA("1-ff00:0:110"))
	require.Error(t, err)
}

// writeMasterKeyPair creates per-AS master key files in the expected directory layout.
func writeMasterKeyPair(t *testing.T, root string, ia addr.IA, key0, key1 []byte) {
	t.Helper()
	asDir := addr.FormatAS(ia.AS(), addr.WithDefaultPrefix(), addr.WithFileSeparator())
	keysDir := filepath.Join(root, asDir, "keys")
	require.NoError(t, os.MkdirAll(keysDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(keysDir, "master0.key"),
		[]byte(base64.StdEncoding.EncodeToString(key0)),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(keysDir, "master1.key"),
		[]byte(base64.StdEncoding.EncodeToString(key1)),
		0o644,
	))
}

// requireManualDerivationMatchesClient checks that manual SV derivation matches client output.
// The dirs argument must contain {"rootDirectory", "ASDirectory"}, e.g. {"gen", "ASff00_0_110"}.
func requireManualDerivationMatchesClient(t *testing.T, dirs [2]string, clientSV []byte) {
	t.Helper()
	masterSecretFilePath := []string{
		dirs[0],
		dirs[1],
		"keys",
		"master0.key",
	}
	master0Path := filepath.Join(masterSecretFilePath...)
	raw, err := os.ReadFile(master0Path)
	require.NoError(t, err)
	master0, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	require.NoError(t, err)

	manualSV := hummlib.DeriveSecretValue(master0)
	require.Equal(t, manualSV, clientSV)
}

// TestParseHummingbirdFlag covers the bandwidths of the -hummingbird flag: with
// a marketplace they are bandwidths and carry a unit, with the secret values of
// the ASes they are bandwidth classes and carry none.
func TestParseHummingbirdFlag(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		raw       string
		withUnits bool
		expected  hummingbirdParameters
		assertErr require.ErrorAssertionFunc
	}{
		"class, no reverse": {
			raw:       "3,5s",
			expected:  hummingbirdParameters{Bw: 3, Duration: 5},
			assertErr: require.NoError,
		},
		"class, with reverse": {
			raw:       "3,5s,2",
			expected:  hummingbirdParameters{Bw: 3, Duration: 5, ReverseBw: 2},
			assertErr: require.NoError,
		},
		"class must not carry a unit": {
			raw:       "3kbps,5s",
			assertErr: require.Error,
		},
		"kbps": {
			raw:       "100kbps,20s",
			withUnits: true,
			expected:  hummingbirdParameters{Bw: 100, Duration: 20},
			assertErr: require.NoError,
		},
		"mbps and gbps": {
			raw:       "1mbps,20s,2gbps",
			withUnits: true,
			expected:  hummingbirdParameters{Bw: 1000, Duration: 20, ReverseBw: 2000000},
			assertErr: require.NoError,
		},
		"units are case insensitive": {
			raw:       "100KBPS,20s,1MBps",
			withUnits: true,
			expected:  hummingbirdParameters{Bw: 100, Duration: 20, ReverseBw: 1000},
			assertErr: require.NoError,
		},
		"bandwidth needs a unit": {
			raw:       "100,20s",
			withUnits: true,
			assertErr: require.Error,
		},
		"reverse bandwidth needs a unit too": {
			raw:       "100kbps,20s,100",
			withUnits: true,
			assertErr: require.Error,
		},
		"unknown unit": {
			raw:       "100tbps,20s",
			withUnits: true,
			assertErr: require.Error,
		},
		"bandwidth beyond 32 bits of kbps": {
			raw:       "5000gbps,20s",
			withUnits: true,
			assertErr: require.Error,
		},
		"missing duration": {
			raw:       "100kbps",
			withUnits: true,
			assertErr: require.Error,
		},
		"duration beyond 16 bits of seconds": {
			raw:       "100kbps,100000s",
			withUnits: true,
			assertErr: require.Error,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			params, err := parseHummingbirdFlag(tc.raw, tc.withUnits)
			tc.assertErr(t, err)
			if err == nil {
				require.Equal(t, tc.expected, params)
			}
		})
	}
}
