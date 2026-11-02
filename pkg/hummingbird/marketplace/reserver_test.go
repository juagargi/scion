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

package marketplace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/snet"
)

// TestReserverReusesConnection checks that the connection to a marketplace is kept between
// calls, that discarding it makes the next call dial anew, and that Close forgets all of them.
func TestReserverReusesConnection(t *testing.T) {
	const api = "https://127.0.0.1:1" // Dialing over TCP is lazy, nothing listens here.
	r := NewReserver("jwt", nil, snet.Topology{}, true)
	ctx := context.Background()

	first, err := r.connection(ctx, api)
	require.NoError(t, err)
	again, err := r.connection(ctx, api)
	require.NoError(t, err)
	assert.Same(t, first, again)

	other, err := r.connection(ctx, "https://127.0.0.1:2")
	require.NoError(t, err)
	assert.NotSame(t, first, other)

	r.discard(api)
	redialed, err := r.connection(ctx, api)
	require.NoError(t, err)
	assert.NotSame(t, first, redialed)
	r.discard("https://unknown") // No-op.

	require.NoError(t, r.Close())
	assert.Empty(t, r.clients)
	after, err := r.connection(ctx, api)
	require.NoError(t, err)
	assert.NotSame(t, redialed, after)
	require.NoError(t, r.Close())
}
