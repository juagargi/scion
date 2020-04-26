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

package scheduler

import (
	"time"

	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
)

type SchedulerInterface interface {
	Init(routerConfig *queues.InternalRouterConfig)
	Dequeuer(routerConfig *queues.InternalRouterConfig, forwarder func(rp *rpkt.RtrPkt))
	Dequeue(queue queues.PacketQueueInterface, forwarder func(rp *rpkt.RtrPkt), queueNo int)
	GetMessages() *chan bool
}

type ScheduleLogger struct {
	incoming  []int
	lastRound []int
	attempted []int
	total     []int

	iterations        int
	t0                time.Time
	tokensUsed        []int
	cirTokens         []int
	pirTokens         []int
	payedIntoSurplus  []int
	forceTake         []int
	overallTokensUsed int
}

func initLogger(length int) ScheduleLogger {
	logger := ScheduleLogger{}
	logger.incoming = make([]int, length)
	logger.lastRound = make([]int, length)
	logger.attempted = make([]int, length)
	logger.total = make([]int, length)
	logger.iterations = 0
	logger.t0 = time.Now()

	logger.tokensUsed = make([]int, length)
	logger.cirTokens = make([]int, length)
	logger.pirTokens = make([]int, length)
	logger.payedIntoSurplus = make([]int, length)
	logger.forceTake = make([]int, length)
	logger.overallTokensUsed = 0

	return logger
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
