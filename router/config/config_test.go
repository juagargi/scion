// Copyright 2019 Anapaya Systems
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

package config_test

import (
	"bytes"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/log/logtest"
	"github.com/scionproto/scion/private/env/envtest"
	apitest "github.com/scionproto/scion/private/mgmtapi/mgmtapitest"
	"github.com/scionproto/scion/router/config"
)

func TestConfigSample(t *testing.T) {
	var sample bytes.Buffer
	var cfg config.Config
	cfg.Sample(&sample, nil, nil)

	InitTestConfig(&cfg)
	err := toml.NewDecoder(bytes.NewReader(sample.Bytes())).DisallowUnknownFields().Decode(&cfg)
	assert.NoError(t, err)
	CheckTestConfig(t, &cfg, config.IDSample)
}

func TestRouterBatchSizeDefaults(t *testing.T) {
	t.Run("production defaults", func(t *testing.T) {
		var cfg config.RouterConfig
		cfg.InitDefaults()
		require.Equal(t, 256, cfg.IngressBatchSize)
		require.Zero(t, cfg.ProcessorQueueSize)
		require.Equal(t, 256, cfg.EgressBatchSize)
		require.Equal(t, 256, cfg.EgressQueueSize)
		require.NoError(t, cfg.Validate())
	})

	t.Run("legacy fallback with override", func(t *testing.T) {
		var cfg config.RouterConfig
		require.NoError(t, toml.Unmarshal([]byte(`
batch_size = 64
egress_batch_size = 1
`), &cfg))
		cfg.InitDefaults()
		require.Equal(t, 64, cfg.IngressBatchSize)
		require.Equal(t, 1, cfg.EgressBatchSize)
		require.Equal(t, 64, cfg.EgressQueueSize)
		require.NoError(t, cfg.Validate())
	})

	t.Run("new keys", func(t *testing.T) {
		var cfg config.RouterConfig
		require.NoError(t, toml.Unmarshal([]byte(`
ingress_batch_size = 63
processor_queue_size = 640
egress_batch_size = 17
egress_queue_size = 65
`), &cfg))
		cfg.InitDefaults()
		require.Equal(t, 63, cfg.IngressBatchSize)
		require.Equal(t, 640, cfg.ProcessorQueueSize)
		require.Equal(t, 17, cfg.EgressBatchSize)
		require.Equal(t, 65, cfg.EgressQueueSize)
	})
}

func TestRouterBatchSizesMustBePositive(t *testing.T) {
	for _, name := range []string{"ingress_batch_size", "egress_batch_size", "egress_queue_size"} {
		t.Run(name, func(t *testing.T) {
			var cfg config.RouterConfig
			require.NoError(t, toml.Unmarshal([]byte(name+" = -1\n"), &cfg))
			cfg.InitDefaults()
			require.Error(t, cfg.Validate())
		})
	}
}

func TestRouterProcessorQueueSizeMustNotBeNegative(t *testing.T) {
	var cfg config.RouterConfig
	require.NoError(t, toml.Unmarshal([]byte("processor_queue_size = -1\n"), &cfg))
	cfg.InitDefaults()
	require.Error(t, cfg.Validate())
}

func InitTestConfig(cfg *config.Config) {
	apitest.InitConfig(&cfg.API)
	envtest.InitTest(&cfg.General, &cfg.Metrics, nil, nil)
	logtest.InitTestLogging(&cfg.Logging)
}

func CheckTestConfig(t *testing.T, cfg *config.Config, id string) {
	apitest.CheckConfig(t, &cfg.API)
	envtest.CheckTest(t, &cfg.General, &cfg.Metrics, nil, nil, id)
	logtest.CheckTestLogging(t, &cfg.Logging, id)
}
