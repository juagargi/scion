// Copyright 2021 Anapaya Systems
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

package colibrisubcmd

import (
	"fmt"

	"github.com/fatih/color"
)

// Config configures the colibri subcommand.
type Config struct {
	// Daemon configures a specific SCION Deamon address.
	Daemon string
	// MaxStitRsvs configures the maximum number of displayed stitchable reservations.
	MaxStitRsvs int
	// MaxFullTrips configures the maximum number of displayed full trips.
	MaxFullTrips int
}

// ColorScheme allows customizing the path coloring.
type ColorScheme struct {
	Header *color.Color
	Keys   *color.Color
	Values *color.Color
	Type   *color.Color
	Link   *color.Color
	Intf   *color.Color
	Good   *color.Color
	Bad    *color.Color
}

func DefaultColorScheme(disable bool) ColorScheme {
	if disable {
		noColor := color.New()
		return ColorScheme{
			Header: noColor,
			Keys:   noColor,
			Values: noColor,
			Type:   noColor,
			Link:   noColor,
			Intf:   noColor,
			Good:   noColor,
			Bad:    noColor,
		}
	}
	return ColorScheme{
		Header: color.New(color.FgHiBlack),
		Keys:   color.New(color.FgHiCyan),
		Values: color.New(),
		Type:   color.New(color.FgYellow, color.Italic),
		Link:   color.New(color.FgHiMagenta),
		Intf:   color.New(color.FgYellow),
		Good:   color.New(color.FgGreen),
		Bad:    color.New(color.FgRed),
	}
}

func (cs ColorScheme) KeyValue(k, v string) string {
	return fmt.Sprintf("%s: %s", cs.Keys.Sprintf(k), cs.Values.Sprintf(v))
}

func (cs ColorScheme) KeyValues(kv ...string) []string {
	if len(kv)%2 != 0 {
		panic("KeyValues expects even number of parameters")
	}
	entries := make([]string, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		entries = append(entries, cs.KeyValue(kv[i], kv[i+1]))
	}
	return entries
}
