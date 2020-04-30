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

package qos

import (
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/qos/scheduler"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/log"
)

const maxNotificationCount = 1024

// Configuration contains the configuration of the qos subsystem
type Configuration struct {
	config         queues.InternalRouterConfig
	schedul        scheduler.SchedulerInterface
	legacyConfig   conf.ExternalConfig
	notifications  chan *queues.NPkt
	worker         workerConfiguration
	workerChannels [](chan *queues.QPkt)
	Forwarder      func(rp *rpkt.RtrPkt)

	droppedPackets int
}

type workerConfiguration struct {
	noWorker   int
	workLength int
}

// SendToWorker sends the Qpkt to the worker responsible for that queue
// serialises all packets belonging to that queue
func (qosConfig *Configuration) SendToWorker(i int, qpkt *queues.QPkt) {
	qosConfig.workerChannels[i] <- qpkt
}

// GetWorkerChannels returns a pointer to an array of all worker channels
func (qosConfig *Configuration) GetWorkerChannels() *[](chan *queues.QPkt) {
	return &qosConfig.workerChannels
}

// GetQueues returns a pointer to an array with all queues
func (qosConfig *Configuration) GetQueues() *[]queues.PacketQueueInterface {
	return &qosConfig.config.Queues
}

// GetQueue returns a pointer to the queue with number ind
func (qosConfig *Configuration) GetQueue(ind int) *queues.PacketQueueInterface {
	return &qosConfig.config.Queues[ind]
}

// GetConfig returns the internal configuration of the border router
func (qosConfig *Configuration) GetConfig() *queues.InternalRouterConfig {
	return &qosConfig.config
}

// GetNotification returns a pointer to the notification channel which contains
// all messages caused by the profile configuration
func (qosConfig *Configuration) GetNotification() chan *queues.NPkt {
	return qosConfig.notifications
}

// SetAndInitSchedul is necessary to set up
// a mock scheduler for testing. Do not use for anything else.
func (qosConfig *Configuration) SetAndInitSchedul(sched scheduler.SchedulerInterface) {
	qosConfig.schedul = sched
	qosConfig.schedul.Init(&qosConfig.config)
}

// InitQos intialises the qos subsystem. It will log and return an error if an error occurs.
func InitQos(extConf conf.ExternalConfig, forwarder func(rp *rpkt.RtrPkt)) (
	Configuration, error) {

	qConfig := Configuration{}
	var err error
	if err = ConvExternalToInternalConfig(&qConfig, extConf); err != nil {
		log.Error("InitQos: Initialising the classification data structures has failed", "error", err)
	}
	if err = InitClassification(&qConfig); err != nil {
		log.Error("InitQos: Initialising the classification data structures has failed", "error", err)
	}
	if err = initScheduler(&qConfig, forwarder); err != nil {
		log.Error("InitQos: Initialising the scheduler has failed", "error", err)
	}
	if err = initWorkers(&qConfig); err != nil {
		log.Error("InitQos: Initialising the workers has failed", "error", err)
	}

	return qConfig, err
}

// ConvExternalToInternalConfig converts the configuration loaded from a file to the
// internal configuration used by the qos subsystem
func ConvExternalToInternalConfig(qConfig *Configuration, extConf conf.ExternalConfig) error {
	var err error
	qConfig.config, err = convertExternalToInteral(extConf)
	qConfig.legacyConfig = extConf
	return err
}

// InitClassification converts the rules to the maps and initialises the cache for
// frequently used rules
func InitClassification(qConfig *Configuration) error {
	qConfig.config.Rules = *queues.RulesToMap(qConfig.config.Rules.RulesList)
	qConfig.config.Rules.CrCache.Init(256)

	return nil
}

func initScheduler(qConfig *Configuration, forwarder func(rp *rpkt.RtrPkt)) error {
	qConfig.notifications = make(chan *queues.NPkt, maxNotificationCount)
	qConfig.Forwarder = forwarder
	// qConfig.schedul = &scheduler.RoundRobinScheduler{}
	qConfig.schedul = &scheduler.WeightedRoundRobinScheduler{}
	// qConfig.schedul = &scheduler.RateRoundRobinScheduler{}
	qConfig.schedul.Init(&qConfig.config)
	go qConfig.schedul.Dequeuer(&qConfig.config, qConfig.Forwarder)

	return nil
}

func initWorkers(qConfig *Configuration) error {
	noWorkers := len(qConfig.config.Queues)
	qConfig.worker = workerConfiguration{noWorkers, 256}
	qConfig.workerChannels = make([]chan *queues.QPkt, qConfig.worker.noWorker)

	for i := range qConfig.workerChannels {
		qConfig.workerChannels[i] = make(chan *queues.QPkt, qConfig.worker.workLength)

		go worker(qConfig, &qConfig.workerChannels[i])
	}

	return nil
}

// QueuePacket is called from router.go and is the first step in the qos subsystem
// it is thread safe (necessary bc. of multiple sockets in the border router).
func (qosConfig *Configuration) QueuePacket(rp *rpkt.RtrPkt) {
	rc := queues.RegularClassRule{}
	config := qosConfig.GetConfig()

	rule := rc.GetRuleForPacket(config, rp)

	queueNo := 0
	if rule != nil {
		queueNo = rule.QueueNumber
	}

	qp := queues.QPkt{Rp: rp, QueueNo: queueNo}

	qosConfig.SendToWorker(queueNo, &qp)
}

func worker(qosConfig *Configuration, workChannel *chan *queues.QPkt) {
	var qp *queues.QPkt
	for {
		qp = <-*workChannel
		queueNo := qp.QueueNo
		putOnQueue(qosConfig, queueNo, qp)
	}
}

// putOnQueue puts the packet on the queue indicated by queueNo. This is not thread safe
// (Police is not). Make sure that there is only ever one worker per queue.
func putOnQueue(qosConfig *Configuration, queueNo int, qp *queues.QPkt) {
	polAct := qosConfig.config.Queues[queueNo].Police(qp)
	profAct := qosConfig.config.Queues[queueNo].CheckAction()

	act := queues.ReturnAction(polAct, profAct)

	switch act {
	case conf.PASS:
		qosConfig.config.Queues[queueNo].Enqueue(qp)
	case conf.NOTIFY:
		qosConfig.config.Queues[queueNo].Enqueue(qp)
		qosConfig.SendNotification(qp)
	case conf.DROPNOTIFY:
		qosConfig.dropPacket(qp)
		qosConfig.SendNotification(qp)
	case conf.DROP:
		qosConfig.dropPacket(qp)
	default:
		qosConfig.config.Queues[queueNo].Enqueue(qp)
	}

	select {
	case *qosConfig.schedul.GetMessages() <- true:
	default:
	}
}

// SendNotification is needed for the part of @stygerma
func (qosConfig *Configuration) SendNotification(qp *queues.QPkt) {
}

func (qosConfig *Configuration) dropPacket(qp *queues.QPkt) {
	defer qp.Rp.Release()

	qosConfig.droppedPackets++
	log.Info("Dropping packet", "qosConfig.droppedPackets", qosConfig.droppedPackets)
	var queLen = make([]int, len(*qosConfig.GetQueues()))
	for i := 0; i < len(*qosConfig.GetQueues()); i++ {
		queLen[i] = (*qosConfig.GetQueue(i)).GetLength()
	}
	log.Info("DROPSTAT", "queueLengths", queLen)
}

func convertExternalToInteral(extConf conf.ExternalConfig) (queues.InternalRouterConfig, error) {
	var internalRules []queues.InternalClassRule
	var internalQueues []queues.PacketQueueInterface

	rc := extConf

	for _, rule := range rc.ExternalRules {
		intRule, err := queues.ConvClassRuleToInternal(rule)
		if err != nil {
			log.Error("Error reading config file", "error", err)
		}
		internalRules = append(internalRules, intRule)
	}

	log.Trace("Loop over Rules")
	for _, iq := range internalRules {
		log.Trace("We have gotten the rule", "rule", iq)
	}

	var intQue queues.PacketQueue
	for _, extQue := range rc.ExternalQueues {

		muta := &sync.Mutex{}
		mutb := &sync.Mutex{}

		queueToUse := &queues.ChannelPacketQueue{}

		intQue = convertExternalToInteralQueue(extQue)
		queueToUse.InitQueue(intQue, muta, mutb)
		internalQueues = append(internalQueues, queueToUse)
	}

	log.Trace("Loop over queues")
	for _, iq := range internalQueues {
		log.Trace("We have gotten the queue", "queue", iq.GetPacketQueue().Name)
	}

	bw := convStringToNumber(rc.SchedulerConfig.Bandwidth)

	bw = bw / 8 // Convert bits to bytes

	log.Debug("We have bandwidth", "bw", bw)

	sc := queues.SchedulerConfig{Latency: rc.SchedulerConfig.Latency, Bandwidth: bw}

	return queues.InternalRouterConfig{
		Scheduler: sc,
		Queues:    internalQueues,
		Rules: queues.MapRules{
			RulesList: internalRules}}, nil

}

func convertExternalToInteralQueue(extQueue conf.ExternalPacketQueue) queues.PacketQueue {
	pq := queues.PacketQueue{
		Name:         extQueue.Name,
		ID:           extQueue.ID,
		MinBandwidth: extQueue.MinBandwidth,
		MaxBandWidth: extQueue.MaxBandWidth,
		PoliceRate:   convStringToNumber(extQueue.PoliceRate),
		MaxLength:    extQueue.MaxLength,
		Priority:     extQueue.Priority,
		Profile:      convertActionProfiles(extQueue.Profile),
	}

	return pq
}
func convertActionProfiles(externalActionProfile []conf.ActionProfile) []queues.ActionProfile {
	ret := make([]queues.ActionProfile, 0)
	for _, prof := range externalActionProfile {
		ret = append(ret, convertActionProfile(prof))
	}
	return ret
}

func convertActionProfile(externalActionProfile conf.ActionProfile) queues.ActionProfile {
	ap := queues.ActionProfile{
		FillLevel: externalActionProfile.FillLevel,
		Prob:      externalActionProfile.Prob,
		Action:    convertPoliceAction(externalActionProfile.Action),
	}
	return ap
}

func convertPoliceAction(externalPoliceAction conf.PoliceAction) conf.PoliceAction {
	return conf.PoliceAction(externalPoliceAction)
}

func convStringToNumber(bandwidthstring string) int {
	prefixes := map[string]int{
		"h": 2,
		"k": 3,
		"M": 6,
		"G": 9,
		"T": 12,
		"P": 15,
		"E": 18,
		"Z": 21,
		"Y": 24,
	}

	var num, powpow int

	for ind, str := range bandwidthstring {
		if val, contains := prefixes[string(str)]; contains {
			powpow = val
			num, _ = strToInt(bandwidthstring[:ind])
			return int(float64(num) * math.Pow(10, float64(powpow)))
		}
	}

	val, _ := strToInt(bandwidthstring)

	return val
}

func strToInt(str string) (int, error) {
	nonFractionalPart := strings.Split(str, ".")
	return strconv.Atoi(nonFractionalPart[0])
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
