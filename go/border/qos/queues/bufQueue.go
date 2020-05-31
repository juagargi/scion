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

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/lib/ringbuf"
)

// PacketBufQueue is a queue based on the ringbuffer from go/lib/ringbuf/rinbguf.go
// it is not used because channelQueue.go is faster.
type PacketBufQueue struct {
	pktQue   PacketQueue
	mutex    *sync.Mutex
	bufQueue *ringbuf.Ring
	length   int
	tb       TokenBucket
}

var _ PacketQueueInterface = (*PacketBufQueue)(nil)

// InitQueue initializes the queue.
// This needs to be called before the queue is used.
func (pq *PacketBufQueue) InitQueue(que PacketQueue, mutQue *sync.Mutex, mutTb *sync.Mutex) {
	pq.pktQue = que
	pq.mutex = mutQue
	pq.length = 0
	pq.tb = TokenBucket{}
	pq.tb.Init(pq.pktQue.PoliceRate)
	pq.bufQueue = ringbuf.New(pq.pktQue.MaxLength, func() interface{} {
		return &QPkt{}
	}, pq.pktQue.Name)
}

// Enqueue enqueues a single pointer to a QPkt
func (pq *PacketBufQueue) Enqueue(rp *QPkt) {
	pq.bufQueue.Write(ringbuf.EntryList{rp}, false)
}

func (pq *PacketBufQueue) canDequeue() bool {
	return pq.GetLength() > 0
}

// GetFillLevel returns the filllevel of the queue in percent
func (pq *PacketBufQueue) GetFillLevel() int {
	return int(float64(pq.GetLength()) / float64(pq.pktQue.MaxLength) * 100)
}

// GetCapacity returns the capacity i.e. the maximum number of
// items on this queue
func (pq *PacketBufQueue) GetCapacity() int {
	return pq.pktQue.MaxLength
}

// GetLength returns the number of packets currently on the queue
// It is thread safe as the underlying ring buffer is thread safe as well.
func (pq *PacketBufQueue) GetLength() int {
	return pq.bufQueue.Readable()
}

// Pop returns the packet from the front of the queue and removes it from the queue
func (pq *PacketBufQueue) Pop() *QPkt {
	pkts := make(ringbuf.EntryList, 1)
	_, _ = pq.bufQueue.Read(pkts, false)
	return pkts[0].(*QPkt)
}

// PopMultiple returns multiple packets from the front of the queue
// and removes them from the queue
func (pq *PacketBufQueue) PopMultiple(number int) []*QPkt {
	pkts := make(ringbuf.EntryList, number)
	_, _ = pq.bufQueue.Read(pkts, false)
	retArr := make([]*QPkt, number)
	for k, pkt := range pkts {
		retArr[k] = pkt.(*QPkt)
	}
	return retArr
}

// CheckAction checks how full the queue is and whether a profile
// has been configured for this fullness.
// If the rule should only be applied with a certain probability
// (for fairness reasons) the random number will be
// used to determine whether it should match or not.
// In some benchmarks rand.Intn() has shown up as bottleneck
// in this function.
// A faster but less random random number might be fine as well.
func (pq *PacketBufQueue) CheckAction() conf.PoliceAction {
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
func (pq *PacketBufQueue) Police(qp *QPkt) conf.PoliceAction {
	return pq.tb.PoliceBucket(qp)
}

// GetMinBandwidth returns the minimum bandwidth / committed information rate associated with this
// queue as configured in the configuration file.
// This is used for the two-rate three-color conditioned scheduler.
// For a more detailed description check the two-rate three-color Conditioned Scheduler paragraph in section 3.2.4 of the report.
func (pq *PacketBufQueue) GetMinBandwidth() int {
	return pq.pktQue.MinBandwidth
}

// GetMaxBandwidth returns the maximum bandwidth / peak information rate associated with this
// queue as configured in the configuration file.
// This is used for the two-rate three-color conditioned scheduler.
// For a more detailed description check the two-rate three-color Conditioned Scheduler paragraph
// in section 3.2.4 of the report.
func (pq *PacketBufQueue) GetMaxBandwidth() int {
	return pq.pktQue.MaxBandWidth
}

// GetPriority returns the priority associated with this queue as configured in the
// configuration file.
// This is used for the weighted round robin scheduler.
// For a more deatiled description check the wheighted round robin scheduler paragraph in
// section 3.2.4 of the report.
func (pq *PacketBufQueue) GetPriority() int {
	return pq.pktQue.Priority
}

// GetPacketQueue returns the PacketQueue struct associated with this queue
func (pq *PacketBufQueue) GetPacketQueue() PacketQueue {
	return pq.pktQue
}
