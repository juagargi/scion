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
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/marketplace"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
)

// TestSearchAssetsReportsHasMore pages through a real database with several page sizes.
// Every page but the last must say that more follow, the last one must say that none do
// (so the client needs no extra request to find out), the pages must not overlap,
// and the peeked asset must be handed out exactly once, on the next page.
func TestSearchAssetsReportsHasMore(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewStorage(
		storage.DBConfig{Connection: "file:" + filepath.Join(t.TempDir(), "marketplace.db")},
		0, 0, 0, 0)
	require.NoError(t, err)
	ia := addr.MustParseIA("1-ff00:0:110")
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	const numAssets = 5
	for i := 0; i < numAssets; i++ {
		_, err := store.PublishAsset(ctx, &db.DBAsset{
			IA:              ia,
			Bandwidth:       100,
			BandwidthMin:    10,
			BandwidthMax:    200,
			Price:           5,
			TimeGranularity: 60,
			TimeMinDuration: 60,
			TimeMaxDuration: 3600,
			StartAt:         start,
			StopsAt:         start.Add(time.Hour),
		})
		require.NoError(t, err)
	}

	testCases := map[string]struct {
		pageSize uint32
		// the number of assets on every page, in order
		pages []int
	}{
		"the last page is partial":   {pageSize: 2, pages: []int{2, 2, 1}},
		"the last page is full":      {pageSize: 5, pages: []int{5}},
		"a page of one asset":        {pageSize: 1, pages: []int{1, 1, 1, 1, 1}},
		"a page larger than the set": {pageSize: 64, pages: []int{5}},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			svc := marketplace.NewSearchService(store, tc.pageSize)
			seen := map[string]bool{}
			for page, want := range tc.pages {
				rep, err := svc.SearchAssetsAs(ia, &hummingbird.SearchAssetsRequest{
					Page: uint32(page),
				})
				require.NoError(t, err)
				require.Len(t, rep.Assets, want, "page %d", page)
				assert.Equal(t, page < len(tc.pages)-1, rep.HasMore, "page %d", page)
				for _, a := range rep.Assets {
					assert.False(t, seen[string(a.AssetId)], "asset repeated on page %d", page)
					seen[string(a.AssetId)] = true
				}
			}
			assert.Len(t, seen, numAssets)
		})
	}

	t.Run("the request can shrink the page", func(t *testing.T) {
		svc := marketplace.NewSearchService(store, 64)
		smaller := uint32(2)
		rep, err := svc.SearchAssetsAs(ia, &hummingbird.SearchAssetsRequest{
			MaxReturnedAssets: &smaller,
		})
		require.NoError(t, err)
		assert.Len(t, rep.Assets, 2)
		assert.True(t, rep.HasMore)
	})
}
