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

package hummingbird

import (
	"sort"
	"strings"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// ASFilter selects which on-path ASes get a flyover.
// The zero value, and a nil *ASFilter, select every AS.
// The listed ASes are the only ones selected, or, when negated, the only ones not selected:
// a filter is never a mix of the two.
type ASFilter struct {
	ases    map[addr.IA]struct{}
	negated bool // the ases are the ones NOT selected
}

// ParseASFilter parses the -partial flag value. An empty string yields a filter
// that selects every AS. It rejects a list that mixes negated and non-negated entries.
func ParseASFilter(raw string) (*ASFilter, error) {
	f := &ASFilter{}
	if strings.TrimSpace(raw) == "" {
		return f, nil
	}
	f.ases = make(map[addr.IA]struct{})
	var neg, pos int
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return nil, serrors.New("empty entry in AS filter", "filter", raw)
		}
		negated := strings.HasPrefix(tok, "!")
		if negated {
			tok = strings.TrimSpace(tok[1:])
			if tok == "" {
				return nil, serrors.New("'!' without an IA in AS filter", "filter", raw)
			}
			neg++
		} else {
			pos++
		}
		ia, err := addr.ParseIA(tok)
		if err != nil {
			return nil, serrors.Wrap("parsing IA in AS filter", err, "entry", tok)
		}
		f.ases[ia] = struct{}{}
	}
	if neg > 0 && pos > 0 {
		return nil, serrors.New(
			"AS filter entries must be either all positive or all negated", "filter", raw)
	}
	f.negated = neg > 0
	return f, nil
}

// Selects reports whether ia gets a flyover.
func (f *ASFilter) Selects(ia addr.IA) bool {
	if f == nil || len(f.ases) == 0 {
		return true
	}
	_, listed := f.ases[ia]
	return listed != f.negated
}

// SelectIndices returns, in path order, the indices of hops whose AS is selected.
// The result is never nil, but is empty if no hop is selected.
func (f *ASFilter) SelectIndices(hops []snetpath.BaseHop) []int {
	indices := make([]int, 0, len(hops))
	for i, h := range hops {
		if f.Selects(h.IA) {
			indices = append(indices, i)
		}
	}
	return indices
}

// MarketSelection is SelectIndices in the form the marketplace takes it: nil, meaning every
// AS of the path, when the filter selects every hop (or is nil), so that the request is the
// same as without a filter. Otherwise it is the selection, which is empty but never nil if
// no hop is selected.
func (f *ASFilter) MarketSelection(hops []snetpath.BaseHop) []int {
	selected := f.SelectIndices(hops)
	if len(selected) == len(hops) {
		return nil
	}
	return selected
}

// PickHops returns the hops at the given indices, in order.
func PickHops(hops []snetpath.BaseHop, indices []int) []snetpath.BaseHop {
	picked := make([]snetpath.BaseHop, 0, len(indices))
	for _, i := range indices {
		picked = append(picked, hops[i])
	}
	return picked
}

// String renders the filter back, for logging.
func (f *ASFilter) String() string {
	if f == nil || len(f.ases) == 0 {
		return "all"
	}
	ias := f.sortedIAs()
	strs := make([]string, len(ias))
	for i, ia := range ias {
		strs[i] = ia.String()
	}
	prefix := "only "
	if f.negated {
		prefix = "all except "
	}
	return prefix + strings.Join(strs, ",")
}

func (f *ASFilter) sortedIAs() []addr.IA {
	ias := make([]addr.IA, 0, len(f.ases))
	for ia := range f.ases {
		ias = append(ias, ia)
	}
	sort.Slice(ias, func(i, j int) bool { return ias[i] < ias[j] })
	return ias
}

// ExpandFlyovers places the flyovers bought/derived for the selected indices back into a
// full-length sequence, leaving nil where no flyover was obtained.
func ExpandFlyovers(
	pathLen int,
	indices []int,
	flyovers []*snetpath.Hop,
) (snetpath.FlyoverSequence, error) {
	if len(indices) != len(flyovers) {
		return nil, serrors.New("indices and flyovers differ in length",
			"indices", len(indices), "flyovers", len(flyovers))
	}
	seq := make(snetpath.FlyoverSequence, pathLen)
	prev := -1
	for k, idx := range indices {
		if idx < 0 || idx >= pathLen {
			return nil, serrors.New("flyover index out of range",
				"index", idx, "path_len", pathLen)
		}
		if idx <= prev {
			return nil, serrors.New("flyover indices not strictly ascending",
				"index", idx, "previous", prev)
		}
		prev = idx
		seq[idx] = flyovers[k]
	}
	return seq, nil
}

// MirrorIndices maps forward selection indices to the indices of the reversed hop list.
func MirrorIndices(pathLen int, indices []int) []int {
	out := make([]int, len(indices))
	for i, idx := range indices {
		out[len(indices)-1-i] = pathLen - 1 - idx
	}
	sort.Ints(out)
	return out
}
