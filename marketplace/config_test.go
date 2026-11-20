// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package marketplace_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/scionproto/scion/marketplace"
)

func TestConfigTopologyFile(t *testing.T) {
	configDir := filepath.Join("etc", "marketplace")
	tests := map[string]struct {
		topology string
		want     string
	}{
		"default is topology.json in config_dir": {
			want: filepath.Join(configDir, "topology.json"),
		},
		"absolute override": {
			topology: "/etc/scion/topology.json",
			want:     "/etc/scion/topology.json",
		},
		"relative override is not joined with config_dir": {
			topology: "scion/topology.json",
			want:     "scion/topology.json",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var cfg marketplace.Config
			cfg.General.ConfigDir = configDir
			cfg.Marketplace.Topology = tc.topology
			assert.Equal(t, tc.want, cfg.TopologyFile())
		})
	}
}
