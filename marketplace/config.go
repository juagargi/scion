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

package marketplace

import (
	"io"

	marketplacestorage "github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/private/config"
	"github.com/scionproto/scion/private/env"
)

// DefaultAddr is where both APIs listen: the TCP one, serving the ConnectRPC API
// and the web app from a single mux, and the SCION one, which is UDP. They do not
// collide. Port 31888 is inside the default dispatched_ports range of an AS, so
// that the border router delivers SCION packets to it without a shim dispatcher.
const DefaultAddr = "localhost:31888"

type Config struct {
	General       env.General                 `toml:"general,omitempty"`
	Logging       log.Config                  `toml:"log,omitempty"`
	Marketplace   MarketplaceConfig           `toml:"marketplace,omitempty"`
	MarketplaceDB marketplacestorage.DBConfig `toml:"marketplace_db,omitempty"`
}

func (cfg *Config) InitDefaults() {
	config.InitAll(
		&cfg.General,
		&cfg.Logging,
		&cfg.Marketplace,
	)
}
func (cfg *Config) Validate() error {
	return config.ValidateAll(
		&cfg.General,
		&cfg.Logging,
		&cfg.Marketplace,
	)
}
func (cfg *Config) Sample(dst io.Writer, path config.Path, _ config.CtxMap) {

}

type MarketplaceConfig struct {
	APIAddr                      string  `toml:"api_addr,omitempty"`
	SCIONAPIAddr                 string  `toml:"scion_api_addr,omitempty"`
	Currency                     string  `toml:"currency,omitempty"`
	StatisticsTimeGranularity    uint32  `toml:"statistics_time_granularity,omitempty"`
	SupportsRedemptionDelegation bool    `toml:"supports_redemption_delegation,omitempty"`
	CurrencyExponent             uint32  `toml:"currency_exponent,omitempty"`
	TransactionFeeRelative       float32 `toml:"transaction_fee_relative,omitempty"`
	TransactionFeeAbsolute       uint64  `toml:"transaction_fee_absolute,omitempty"`
	SplitCombineFeeAbsolute      uint64  `toml:"split_combine_fee_absolute,omitempty"`
	DelegationHourlyFee          uint64  `toml:"delegation_hourly_fee,omitempty"`
	DisableUserRegistration      bool    `toml:"disable_user_registration,omitempty"`
	DisableASRegistration        bool    `toml:"disable_as_registration,omitempty"`
	MaxRowsPerPage               uint32  `toml:"max_rows_per_page,omitempty"`
	AssetValidityMax             uint32  `toml:"asset_validity_max,omitempty"`
}

func (cfg *MarketplaceConfig) InitDefaults() {
	if cfg.APIAddr == "" {
		cfg.APIAddr = DefaultAddr
	}
	if cfg.SCIONAPIAddr == "" {
		cfg.SCIONAPIAddr = DefaultAddr
	}
	if cfg.Currency == "" {
		cfg.Currency = "CHF"
	}
	if cfg.StatisticsTimeGranularity == 0 {
		cfg.StatisticsTimeGranularity = 86400
	}
	if cfg.MaxRowsPerPage == 0 {
		cfg.MaxRowsPerPage = 64
	}
	if cfg.AssetValidityMax == 0 {
		cfg.AssetValidityMax = 60 * 60 * 24 * 365 // 1 year
	}
}
func (cfg *MarketplaceConfig) Validate() error {
	return nil
}
func (cfg *MarketplaceConfig) Sample(dst io.Writer, path config.Path, _ config.CtxMap) {

}
