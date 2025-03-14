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

package aliasdb

import (
	"context"
	"database/sql"
	"io"
	"time"

	"github.com/scionproto/scion/pkg/alias"
)

type DB interface {
	io.Closer
	ReadWrite
	BeginTransaction(ctx context.Context, opts *sql.TxOptions) (Transaction, error)
}

type ReadWrite interface {
	Get(ctx context.Context, hostname string) ([]alias.Replica, error)
	AddReplicas(ctx context.Context, hostname string, replicas []alias.Replica) error
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
	Delete(ctx context.Context, hostname string, replicas []alias.Replica) (int, error)
	DeleteAll(ctx context.Context, hostname string) (int, error)
}

type Transaction interface {
	ReadWrite
	Commit() error
	Rollback() error
}
