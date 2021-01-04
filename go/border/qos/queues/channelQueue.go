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

package queues

import (
	"math/rand"
	"sync"
	"time"

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/scmp"
)

// ChannelPacketQueue is a queue of Qpkts based on Go channels
type ChannelPacketQueue struct {
	pktQue PacketQueue

	mutex *sync.Mutex

	queue chan *QPkt
	tb    TokenBucket
	pid   scmp.PID
}

var _ PacketQueueInterface = (*ChannelPacketQueue)(nil)

// InitQueue initializes the queue.
// This needs to be called before the queue is used.
func (pq *ChannelPacketQueue) InitQueue(que PacketQueue, mutQue *sync.Mutex, mutTb *sync.Mutex) {
	pq.pktQue = que
	pq.mutex = mutQue
	pq.tb = TokenBucket{}
	pq.tb.Init(pq.pktQue.PoliceRate)
	pq.queue = make(chan *QPkt, pq.pktQue.MaxLength+1)
	if pq.pktQue.CongestionWarning.Approach == 2 {
		pq.pid = scmp.PID{FactorProportional: .5, FactorIntegral: 0.6,
			FactorDerivative: .3, LastUpdate: time.Now(), SetPoint: 70,
			Min: 60, Max: 90}
	}
}

// Enqueue enqueues a single pointer to a QPkt
func (pq *ChannelPacketQueue) Enqueue(rp *QPkt) {
	pq.queue <- rp
}

func (pq *ChannelPacketQueue) canEnqueue() bool {
	return int(len(pq.queue)) < pq.pktQue.MaxLength
}

func (pq *ChannelPacketQueue) canDequeue() bool {
	return true
}

// GetFillLevel returns the filllevel of the queue in percent
func (pq *ChannelPacketQueue) GetFillLevel() int {
	return int(float64(len(pq.queue)) / float64(pq.pktQue.MaxLength) * 100)
}

// GetCapacity returns the capacity i.e. the maximum number of
// items on this queue
func (pq *ChannelPacketQueue) GetCapacity() int {
	return pq.pktQue.MaxLength
}

// GetLength returns the number of packets currently on the queue
// It is thread safe as the underlying ring buffer is thread safe as well.
func (pq *ChannelPacketQueue) GetLength() int {

	return int(len(pq.queue))
}

func (pq *ChannelPacketQueue) peek() *QPkt {

	return nil
}

// Pop returns the packet from the front of the queue and removes it from the queue
// It is thread safe as the Go channel used internally is thread safe.
func (pq *ChannelPacketQueue) Pop() *QPkt {

	var pkt *QPkt

	select {
	case pkt = <-pq.queue:
	default:
		pkt = nil
	}
	return pkt
}

// PopMultiple returns multiple packets from the front of the queue
// and removes them from the queue/
// It is not thread safe.
func (pq *ChannelPacketQueue) PopMultiple(number int) []*QPkt {

	pkts := make([]*QPkt, number)

	for i := 0; i < number; i++ {
		pkts[i] = <-pq.queue
	}

	return pkts
}

// CheckAction checks how full the queue is and whether a profile
// has been configured for this fullness.
// If the rule should only be applied with a certain probability
// (for fairness reasons) the random number will be
// used to determine whether it should match or not.
// In some benchmarks rand.Intn() has shown up as bottleneck
// in this function.
// A faster but less random random number might be fine as well.
func (pq *ChannelPacketQueue) CheckAction() conf.PoliceAction {

	if pq.pktQue.MaxLength <= pq.GetLength() {
		log.Trace("Queue is at max capacity", "queueNo", pq.pktQue.ID)
		return conf.DROPNOTIFY
	}

	level := pq.GetFillLevel()

	for j := len(pq.pktQue.Profile) - 1; j >= 0; j-- {
		if level >= pq.pktQue.Profile[j].FillLevel {
			if rand.Intn(100) < (pq.pktQue.Profile[j].Prob) {
				return pq.pktQue.Profile[j].Action
			}
		}
	}
	return conf.PASS
}

// Police returns the decision from the policer whether the packet can be enqueued or dequeued.
// Section 3.2.2 and 4.4 of the report contain a more detailed description of the policer
func (pq *ChannelPacketQueue) Police(qp *QPkt) conf.PoliceAction {
	return pq.tb.PoliceBucket(qp)
}

// GetMinBandwidth returns the minimum bandwidth / committed information rate associated with this
// queue as configured in the configuration file.
// This is used for the two-rate three-color conditioned scheduler.
// For a more detailed description check the two-rate three-color Conditioned Scheduler paragraph in section 3.2.4 of the report.
func (pq *ChannelPacketQueue) GetMinBandwidth() int {
	return pq.pktQue.MinBandwidth
}

// GetMaxBandwidth returns the maximum bandwidth / peak information rate associated with this
// queue as configured in the configuration file.
// This is used for the two-rate three-color conditioned scheduler.
// For a more detailed description check the two-rate three-color Conditioned Scheduler paragraph
// in section 3.2.4 of the report.
func (pq *ChannelPacketQueue) GetMaxBandwidth() int {
	return pq.pktQue.MaxBandWidth
}

// GetPriority returns the priority associated with this queue as configured in the
// configuration file.
// This is used for the weighted round robin scheduler.
// For a more deatiled description check the wheighted round robin scheduler paragraph in
// section 3.2.4 of the report.
func (pq *ChannelPacketQueue) GetPriority() int {
	return pq.pktQue.Priority
}

// GetPacketQueue returns the PacketQueue struct associated with this queue
func (pq *ChannelPacketQueue) GetPacketQueue() PacketQueue {
	return pq.pktQue
}

func (pq *ChannelPacketQueue) GetCongestionWarning() *CongestionWarning {
	return &pq.pktQue.CongestionWarning
}

func (pq *ChannelPacketQueue) GetTokenBucket() *TokenBucket {
	return &pq.tb
}

func (pq *ChannelPacketQueue) GetPID() *scmp.PID {
	return &pq.pid
}
