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
	"github.com/scionproto/scion/go/lib/log"
)

type ChannelPacketQueue struct {
	pktQue PacketQueue

	mutex *sync.Mutex

	queue chan *QPkt
	tb    TokenBucket
}

var _ PacketQueueInterface = (*ChannelPacketQueue)(nil)

func (pq *ChannelPacketQueue) InitQueue(que PacketQueue, mutQue *sync.Mutex, mutTb *sync.Mutex) {
	pq.pktQue = que
	pq.mutex = mutQue
	// pq.length = 0
	pq.tb = TokenBucket{}
	pq.tb.Init(pq.pktQue.PoliceRate)
	pq.queue = make(chan *QPkt, pq.pktQue.MaxLength+100)
}

func (pq *ChannelPacketQueue) Enqueue(rp *QPkt) {
	pq.queue <- rp

	// atomic.AddUint64(&pq.length, 1)

}

func (pq *ChannelPacketQueue) canEnqueue() bool {

	return int(len(pq.queue)) < pq.pktQue.MaxLength
}

func (pq *ChannelPacketQueue) canDequeue() bool {

	return true
}

func (pq *ChannelPacketQueue) GetFillLevel() int {

	return int(len(pq.queue)) / int(pq.pktQue.MaxLength)
}

func (pq *ChannelPacketQueue) GetLength() int {

	return int(len(pq.queue))
}

func (pq *ChannelPacketQueue) peek() *QPkt {

	return nil
}

func (pq *ChannelPacketQueue) Pop() *QPkt {

	var pkt *QPkt

	select {
	case pkt = <-pq.queue:
	default:
		pkt = nil
	}

	// pkt = <-pq.queue

	return pkt
}

func (pq *ChannelPacketQueue) PopMultiple(number int) []*QPkt {

	pkts := make([]*QPkt, number)

	for i := 0; i < number; i++ {
		pkts[i] = <-pq.queue
	}

	return pkts
}

func (pq *ChannelPacketQueue) CheckAction() conf.PoliceAction {

	if pq.pktQue.MaxLength-100 <= pq.GetLength() {
		log.Debug("Queue is at max capacity", "queueNo", pq.pktQue.ID)
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

func (pq *ChannelPacketQueue) Police(qp *QPkt) conf.PoliceAction {
	return pq.tb.PoliceBucket(qp)
}

func (pq *ChannelPacketQueue) GetMaxBandwidth() int {
	return pq.pktQue.MaxBandWidth
}

func (pq *ChannelPacketQueue) GetMinBandwidth() int {
	return pq.pktQue.MinBandwidth
}

func (pq *ChannelPacketQueue) GetPriority() int {
	return pq.pktQue.Priority
}

func (pq *ChannelPacketQueue) GetPacketQueue() PacketQueue {
	return pq.pktQue
}
