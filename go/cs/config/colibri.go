// Copyright 2020 ETH Zurich
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

package config

import (
	"io"

	"github.com/scionproto/scion/go/lib/config"
	"github.com/scionproto/scion/go/pkg/storage"
)

// ColibriConfig is the root configuration for all things reservation.
type ColibriConfig struct {
	DB               storage.DBConfig `toml:"colibri_db,omitempty"`
	Delta            float64
	CapacitiesFile   string `toml:"capacities_file"`   // cs/reservation/conf.Capacities
	ReservationsFile string `toml:"reservations_file"` // cs/reservation/conf.Reservations
}

func (cfg *ColibriConfig) Validate() error {
	return nil
}

func (cfg *ColibriConfig) Sample(dst io.Writer, _ config.Path, _ config.CtxMap) {
	config.WriteString(dst, colibriSample)
}

func (cfg *ColibriConfig) ConfigName() string {
	return "colibri"
}

func (cfg *ColibriConfig) Enabled() bool {
	return cfg.DB.Connection != ""
}
