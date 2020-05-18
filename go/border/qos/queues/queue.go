// Copyright 2020 ETH Zurich
// Copyright 2020 ETH Zurich, Anapaya Systems
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
	"sync"

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/scmp"
)

// QPkt is the representation of a router packet in the qos subsystem
type QPkt struct {
	QueueNo int
	Act     Action
	Rp      *rpkt.RtrPkt
	Forward bool
	Mtx     sync.Mutex
}

// NPkt is the representation of a router packet used when sending notifications
type NPkt struct {
	Rule *InternalClassRule
	Qpkt *QPkt
}

// Violation is the reason for why a certain Action was taken
type Violation int

const (
	None Violation = iota
	BandWidthExceeded
	queueFull
)

type Action struct {
	rule   *InternalClassRule
	reason Violation
	action conf.PoliceAction
}

func (a *Action) GetReason() int {
	reason := a.reason
	return int(reason)
}

func (a *Action) SetAction(newAction conf.PoliceAction) {
	a.action = newAction
}

func (a *Action) GetAction() conf.PoliceAction {
	return a.action
}

type CongestionWarning struct {
	Approach           int `yaml:"approach"`
	InformationContent int `yaml:"informationContent"`
}

type ActionProfile struct {
	FillLevel int               `yaml:"fill-level"`
	Prob      int               `yaml:"prob"`
	Action    conf.PoliceAction `yaml:"action"`
}

type PacketQueue struct {
	Name              string
	ID                int
	MinBandwidth      int
	MaxBandWidth      int
	PoliceRate        int
	MaxLength         int
	Priority          int
	CongestionWarning CongestionWarning
	Profile           []ActionProfile
}

type PacketQueueInterface interface {
	InitQueue(que PacketQueue, mutQue *sync.Mutex, mutTb *sync.Mutex)
	Enqueue(*QPkt)
	Pop() *QPkt
	PopMultiple(number int) []*QPkt
	GetFillLevel() int
	GetLength() int
	CheckAction() conf.PoliceAction
	Police(qp *QPkt) conf.PoliceAction
	GetPriority() int
	GetCapacity() int
	GetMinBandwidth() int
	GetMaxBandwidth() int
	GetPacketQueue() PacketQueue
	GetCongestionWarning() *CongestionWarning
	GetTokenBucket() *TokenBucket
	GetPID() *scmp.PID
}

// MergeAction merges both PoliceAction together and returns the merged result.
func MergeAction(pol conf.PoliceAction, prof conf.PoliceAction) conf.PoliceAction {
	// Check if any of pol or prof actions are DROPNOTIFY, DROP, NOTIFY OR PASS, in this order
	if pol == conf.DROPNOTIFY || prof == conf.DROPNOTIFY {
		return conf.DROPNOTIFY
	} else if pol == conf.DROP || prof == conf.DROP {
		return conf.DROP
	} else if pol == conf.NOTIFY || prof == conf.NOTIFY {
		return conf.NOTIFY
	}
	return conf.PASS
}
