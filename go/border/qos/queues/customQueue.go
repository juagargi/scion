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
)

// CustomPacketQueue is a queue for Qpkts based on a ringbuffer.
// It was only used during development. For documentation please see channelQueue.go or bufQueue.go
type CustomPacketQueue struct {
	pktQue PacketQueue
	mutex  *sync.Mutex
	queue  []*QPkt
	length int
	tb     TokenBucket
	head   int
	tail   int
	mask   int
}

var _ PacketQueueInterface = (*CustomPacketQueue)(nil)

func (pq *CustomPacketQueue) InitQueue(que PacketQueue, mutQue *sync.Mutex, mutTb *sync.Mutex) {
	pq.pktQue = que
	pq.mutex = mutQue
	pq.length = 0
	pq.tb = TokenBucket{}
	pq.tb.Init(pq.pktQue.PoliceRate)
	pq.queue = make([]*QPkt, pq.pktQue.MaxLength)
	pq.head = 0
	pq.tail = 0
	pq.mask = pq.pktQue.MaxLength - 1
}

func (pq *CustomPacketQueue) Enqueue(rp *QPkt) {

	// Making this lockfree makes it 10 times faster
	pq.mutex.Lock()
	defer pq.mutex.Unlock()

	pq.queue[pq.tail] = rp
	pq.tail = (pq.tail + 1) & pq.mask
	pq.length = pq.length + 1
}

func (pq *CustomPacketQueue) canEnqueue() bool {
	return pq.length < pq.pktQue.MaxLength
}

func (pq *CustomPacketQueue) canDequeue() bool {
	return pq.head < pq.tail
}

func (pq *CustomPacketQueue) GetFillLevel() int {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()
	return int(float64(pq.length) / float64(pq.pktQue.MaxLength) * 100)
}

// GetCapacity returns the capacity i.e. the maximum number of
// items on this queue
func (pq *CustomPacketQueue) GetCapacity() int {
	return pq.pktQue.MaxLength
}

func (pq *CustomPacketQueue) GetLength() int {
	return pq.length
}

func (pq *CustomPacketQueue) peek() *QPkt {
	return pq.queue[0]
}

func (pq *CustomPacketQueue) Pop() *QPkt {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()

	pkt := pq.queue[pq.head]
	pq.head = (pq.head + pq.pktQue.MaxLength + 1) & pq.mask
	pq.length = pq.length - 1

	return pkt
}

func (pq *CustomPacketQueue) PopMultiple(number int) []*QPkt {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()

	var pkt []*QPkt
	if pq.head+number < pq.pktQue.MaxLength {
		pkt = pq.queue[pq.head : pq.head+number]
		pq.head = (pq.head + number) & pq.mask

	} else {
		for pq.head+number > pq.pktQue.MaxLength {
			pkt = pq.queue[pq.head:pq.pktQue.MaxLength]
			number = number - (pq.pktQue.MaxLength - pq.head)
			pq.head = 0

			pkt = append(pkt, pq.queue[pq.head:pq.head+number]...)
			pq.head = (pq.head + number) & pq.mask
		}

		pkt = append(pkt, pq.queue[pq.head:pq.head+number]...)
		pq.head = (pq.head + number) & pq.mask
	}
	pq.length = pq.length - number
	return pkt
}

func (pq *CustomPacketQueue) CheckAction() conf.PoliceAction {
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

func (pq *CustomPacketQueue) Police(qp *QPkt) conf.PoliceAction {
	return pq.tb.PoliceBucket(qp)
}

func (pq *CustomPacketQueue) GetMinBandwidth() int {
	return pq.pktQue.MinBandwidth
}

func (pq *CustomPacketQueue) GetMaxBandwidth() int {
	return pq.pktQue.MaxBandWidth
}

func (pq *CustomPacketQueue) GetPriority() int {
	return pq.pktQue.Priority
}

func (pq *CustomPacketQueue) GetPacketQueue() PacketQueue {
	return pq.pktQue
}
