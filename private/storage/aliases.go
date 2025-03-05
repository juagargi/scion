// Copyright 2025 ETH Zurich
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

// Package storage provides factories for various application storage backends.
package storage

import (
	"github.com/scionproto/scion/private/aliasdb"
	sqlitealiasdb "github.com/scionproto/scion/private/storage/alias/sqlite"
)

func NewAliasStorage(c DBConfig) (aliasdb.DB, error) {
	db, err := sqlitealiasdb.New(c.Connection)
	if err != nil {
		return nil, err
	}
	SetConnLimits(db, c)

	// deleteme: TODO cleaner and struct type for alias DB.

	// // Start a periodic task that cleans up the expired path segments.
	// cleaner := periodic.Start(
	// 	cleaner.New(
	// 		func(ctx context.Context) (int, error) {
	// 			return db.DeleteExpired(ctx, time.Now())
	// 		},
	// 		"control_pathstorage_cleaner",
	// 	),
	// 	30*time.Second,
	// 	30*time.Second,
	// )
	// return pathDBWithCleaner{
	// 	DB:       db,
	// 	cleaner:  cleaner,
	// 	dbCloser: db,
	// }, nil

	return db, err
}
