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

// Package config describes the configuration of the hummingbird service.
package config

import (
	"io"
	"time"

	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/private/config"
	"github.com/scionproto/scion/private/env"
	"github.com/scionproto/scion/redemption_server/storage"
)

const (
	// DefaultReservationDuration is the default duration of a hummingbird reservation.
	DefaultReservationDuration = 5 * time.Second
)

var _ config.Config = (*Config)(nil)

// Config is the hummingbird service configuration.
type Config struct {
	General  env.General  `toml:"general,omitempty"`
	Features env.Features `toml:"features,omitempty"`
	Logging  log.Config   `toml:"log,omitempty"`
	Metrics  env.Metrics  `toml:"metrics,omitempty"`
	HB       HBConfig     `toml:"hummingbird,omitempty"`
}

// InitDefaults initializes the default values for all parts of the config.
func (cfg *Config) InitDefaults() {
	config.InitAll(
		&cfg.General,
		&cfg.Features,
		&cfg.Logging,
		&cfg.Metrics,
		&cfg.HB,
	)
}

// Validate validates all parts of the config.
func (cfg *Config) Validate() error {
	return config.ValidateAll(
		&cfg.General,
		&cfg.Features,
		&cfg.Logging,
		&cfg.Metrics,
		&cfg.HB,
	)
}

// Sample generates a sample config file for the hummingbird service.
func (cfg *Config) Sample(dst io.Writer, path config.Path, _ config.CtxMap) {
	config.WriteSample(dst, path, config.CtxMap{config.ID: idSample},
		&cfg.General,
		&cfg.Features,
		&cfg.Logging,
		&cfg.Metrics,
		&cfg.HB,
	)
}

var _ config.Config = (*HBConfig)(nil)

// HBConfig holds the configuration specific to the hummingbird service.
type HBConfig struct {
	Marketplaces []*MarketplaceConfig `toml:"marketplaces,omitempty"`
	RedemptionDB storage.DBConfig     `toml:"redemption_db,omitempty"`
}

type MarketplaceConfig struct {
	Address        string `toml:"address,omitempty"`
	KeySalt        string `toml:"salt,omitempty"`
	ResIdLimitLow  uint32 `toml:"res_id_limit_low,omitempty"`
	ResIdLimitHigh uint32 `toml:"res_id_limit_high,omitempty"`
	Insecure       bool   `toml:"insecure,omitempty"`
}

func (cfg *MarketplaceConfig) Validate() error {
	if cfg.Address == "" {
		return serrors.New("marketplace url not configured")
	}
	if cfg.ResIdLimitHigh <= cfg.ResIdLimitLow {
		return serrors.New("reservation ID limits not configured correctly")
	}
	return nil
}

func (cfg *MarketplaceConfig) InitDefaults() {
	if cfg.KeySalt == "" {
		cfg.KeySalt = hummingbird.SecretValueDerivationSalt
	}
}

// InitDefaults the default values for the durations that are equal to zero.
func (cfg *HBConfig) InitDefaults() {
	for _, marketplace := range cfg.Marketplaces {
		marketplace.InitDefaults()
	}
}

// Validate validates that all durations are set.
func (cfg *HBConfig) Validate() error {
	for _, marketpalce := range cfg.Marketplaces {
		if err := marketpalce.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Sample generates a sample for the hummingbird service specific configuration.
func (cfg *HBConfig) Sample(dst io.Writer, path config.Path, ctx config.CtxMap) {
	config.WriteString(dst, hbSample)
}

// ConfigName is the toml key for the beacon server specific configuration.
func (cfg *HBConfig) ConfigName() string {
	return "hummingbird"
}
