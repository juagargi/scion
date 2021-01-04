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
	"bytes"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/qos/scheduler"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/scmp"
)

const (
	maxNotificationCount = 5120
	sendNotification     = true
)

// Configuration contains the configuration of the qos subsystem
type Configuration struct {
	config             queues.InternalRouterConfig
	schedul            scheduler.SchedulerInterface
	legacyConfig       conf.ExternalConfig
	basicNotifications chan *queues.NPkt
	stochNotifications chan *queues.NPkt
	worker             workerConfiguration
	workerChannels     [](chan *queues.QPkt)
	Forwarder          func(rp *rpkt.RtrPkt)
	Classifier         queues.ClassRuleInterface
	droppedPackets     int
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
func (qosConfig *Configuration) GetBasicNotification() *chan *queues.NPkt {
	return &qosConfig.basicNotifications
}

func (qosConfig *Configuration) GetStochNotification() *chan *queues.NPkt {
	return &qosConfig.stochNotifications
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
		log.Error("InitQos: Initialising the classification data structures has failed",
			"error", err)
	}
	if err = InitClassification(&qConfig); err != nil {
		log.Error("InitQos: Initialising the classification data structures has failed",
			"error", err)
	}
	if err = initScheduler(&qConfig, forwarder); err != nil {
		log.Error("InitQos: Initialising the scheduler has failed",
			"error", err)
	}
	if err = initWorkers(&qConfig); err != nil {
		log.Error("InitQos: Initialising the workers has failed",
			"error", err)
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

	qConfig.Classifier = &queues.CachelessClassRule{}
	qConfig.Classifier.Init(len(qConfig.config.Rules.RulesList))

	return nil
}

func initScheduler(qConfig *Configuration, forwarder func(rp *rpkt.RtrPkt)) error {
	qConfig.basicNotifications = make(chan *queues.NPkt, maxNotificationCount)
	qConfig.stochNotifications = make(chan *queues.NPkt, maxNotificationCount)
	qConfig.Forwarder = forwarder

	qConfig.schedul = &scheduler.WeightedRoundRobinScheduler{}
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
	config := qosConfig.GetConfig()
	rc := qosConfig.Classifier

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

	act := queues.MergeAction(polAct, profAct)

	qp.Act.SetAction(act)
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

	*qosConfig.schedul.GetMessages() <- true
}

// SendNotification is needed for the part of @stygerma
func (qosConfig *Configuration) SendNotification(qp *queues.QPkt) { //COMP:
	// qp.Rp.RefInc(1) //should avoid the packet being dropped before we can create the scmp notification
	// defer qp.Rp.RefInc(-1)
	rc := queues.RegularClassRule{}
	config := qosConfig.GetConfig()

	rule := rc.GetRuleForPacket(config, qp.Rp)
	np := queues.NPkt{Rule: rule, Qpkt: qp}

	//Don't answer CW SCMPs to avoid creating traffic loops
	l4hdrType := np.Qpkt.Rp.L4Type
	log.Debug("Packet in sendNotification", "id", np.Qpkt.Rp.Id)
	// if l4hdrType == common.L4SCMP {
	l4hdr, err := np.Qpkt.Rp.L4Hdr(false)
	if err == nil {
		scmphdr, ok := l4hdr.(*scmp.Hdr)

		// _, ok := l4hdr.(*scmp.Hdr)
		// if scmphdr.Class == scmp.C_General && scmphdr.Type == scmp.T_G_BasicCongWarn && ok {
		// 	np.Qpkt.Rp.RefInc(-1)
		// log.Debug("CW packet should not be notified", "l4", l4hdrType, "scmp hdr", scmphdr)
		// 	return
		// }
		if ok {
			if scmphdr.Class == scmp.C_General && (scmphdr.Type == scmp.T_G_BasicCongWarn || scmphdr.Type == scmp.T_G_StochasticCongWarn) && ok {
				log.Debug("CW packet should not be notified", "l4", l4hdrType, "scmp hdr", scmphdr)

				if uint8(np.Qpkt.Act.GetAction()) == 0 || uint8(np.Qpkt.Act.GetAction()) == 1 { //
					np.Qpkt.Mtx.Lock()
					if np.Qpkt.Forward {
						np.Qpkt.Mtx.Unlock()
						qosConfig.Forwarder(np.Qpkt.Rp)
						log.Debug("CW packet forwarded", "id", qp.Rp.Id)
						return
					}
					np.Qpkt.Forward = true
					np.Qpkt.Mtx.Unlock()
					log.Debug("CW packet forwarding enabled", "id", qp.Rp.Id)
					return
				}

				// Release packet if it's action is DROPNOTIFY
				if uint8(np.Qpkt.Act.GetAction()) == 3 {
					np.Qpkt.Rp.Release()
					log.Debug("CW packet released", "id", qp.Rp.Id)
					return
				}

			}
		}
	} else {
		log.Debug("Error while fetching the L4Hdr", "err", err)
	}
	// srcbr10_1, srcbr10_2,
	// var srccs10, srcsd10, srccs11, srcsd11, srccs12, srcsd12, srccs13, srcsd13, srcbrctrl10_1, srcbrctrl10_2, srcbrctrl11_1, srcbrctrl11_2, srcbrctrl12_1, srcbrctrl12_2, srcbrctrl13_1, srcbrctrl13_2, dstcs10, dstsd10, dstcs11, dstsd11, dstcs12, dstsd12, dstcs13, dstsd13, dstbr10_1, dstbr10_2, dstbr11_1, dstbr11_2, dstbr12_1, dstbr12_2, dstbr13_1, dstbr13_2 bool
	// var srcbr10_1, srcbr10_2, srcbr11_1, srcbr11_2, srcbr12_1, srcbr12_2, srcbr13_1, srcbr13_2, dstbrctrl10_1, dstbrctrl10_2, dstbrctrl11_1, dstbrctrl11_2, dstbrctrl12_1, dstbrctrl12_2, dstbrctrl13_1, dstbrctrl13_2 bool
	var srcIPv4, dstIPv4 bool
	var srcSVC, dstSVC bool
	ipLeast := net.ParseIP("127.0.0.4")
	ipMost := net.ParseIP("127.0.0.44")
	srcHost, err := qp.Rp.SrcHost()
	if err != nil {
		log.Error("Unable to fetch source host in sendNotifications method")
	} else if dstHost, err := qp.Rp.DstHost(); err != nil {
		log.Error("Unable to fetch destination host in sendNotifications method")
	} else {
		if srcHost.Type() == addr.HostTypeIPv4 {
			srcIPv4 = true
			if bytes.Compare(srcHost.IP(), ipLeast) >= 0 && bytes.Compare(srcHost.IP(), ipMost) <= 0 {
				srcSVC = true
			}
			// srcbr10_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.4"))
			// srcbr10_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.11"))
			// srccs10 = srcHost.IP().Equal(net.ParseIP("127.0.0.19"))
			// srcsd10 = srcHost.IP().Equal(net.ParseIP("127.0.0.20"))
			// srcbr11_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.10"))
			// srcbr11_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.11"))
			// srcbrctrl10_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.17"))
			// srcbrctrl10_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.18"))
			// srccs11 = srcHost.IP().Equal(net.ParseIP("127.0.0.27"))
			// srcsd11 = srcHost.IP().Equal(net.ParseIP("127.0.0.28"))
			// srcbrctrl11_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.25"))
			// srcbrctrl11_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.26"))
			// srccs12 = srcHost.IP().Equal(net.ParseIP("127.0.0.35"))
			// srcsd12 = srcHost.IP().Equal(net.ParseIP("127.0.0.36"))
			// srcbrctrl12_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.33"))
			// srcbrctrl12_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.34"))
			// srccs13 = srcHost.IP().Equal(net.ParseIP("127.0.0.43"))
			// srcsd13 = srcHost.IP().Equal(net.ParseIP("127.0.0.44"))
			// srcbrctrl13_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.41"))
			// srcbrctrl13_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.42"))
			// srcbr10_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.4"))
			// srcbr10_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.6"))
			// srcbr11_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.5"))
			// srcbr11_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.8"))
			// srcbr12_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.7"))
			// srcbr12_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.10"))
			// srcbr13_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.9"))
			// srcbr13_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.11"))
		}
		if dstHost.Type() == addr.HostTypeIPv4 {
			dstIPv4 = true
			if bytes.Compare(dstHost.IP(), ipLeast) >= 0 && bytes.Compare(dstHost.IP(), ipMost) <= 0 {
				dstSVC = true
			}
			// dstcs10 = srcHost.IP().Equal(net.ParseIP("127.0.0.19"))
			// dstsd10 = srcHost.IP().Equal(net.ParseIP("127.0.0.20"))
			// dstbrctrl10_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.17"))
			// dstbrctrl10_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.18"))
			// dstcs11 = srcHost.IP().Equal(net.ParseIP("127.0.0.27"))
			// dstsd11 = srcHost.IP().Equal(net.ParseIP("127.0.0.28"))
			// dstbrctrl11_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.25"))
			// dstbrctrl11_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.26"))
			// dstcs12 = srcHost.IP().Equal(net.ParseIP("127.0.0.35"))
			// dstsd12 = srcHost.IP().Equal(net.ParseIP("127.0.0.36"))
			// dstbrctrl12_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.33"))
			// dstbrctrl12_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.34"))
			// dstcs13 = srcHost.IP().Equal(net.ParseIP("127.0.0.43"))
			// dstsd13 = srcHost.IP().Equal(net.ParseIP("127.0.0.44"))
			// dstbrctrl13_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.41"))
			// dstbrctrl13_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.42"))
			// dstbr10_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.4"))
			// dstbr10_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.6"))
			// dstbr11_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.5"))
			// dstbr11_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.8"))
			// dstbr12_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.7"))
			// dstbr12_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.10"))
			// dstbr13_1 = srcHost.IP().Equal(net.ParseIP("127.0.0.9"))
			// dstbr13_2 = srcHost.IP().Equal(net.ParseIP("127.0.0.11"))
		}

		// if  {
		if srcIPv4 || dstIPv4 {
			// if srcbr10_1 || srcbr10_2 || srcbr11_1 || srcbr11_2 || srcbr12_1 || srcbr12_2 || srcbr13_1 || srcbr13_2 || dstbr10_1 || dstbr10_2 || dstbr11_1 || dstbr11_2 || dstbr12_1 || dstbr12_2 || dstbr13_1 || dstbr13_2 || srccs10 || srcsd10 || srccs11 || srcsd11 || srccs12 || srcsd12 || srccs13 || srcsd13 || srcbrctrl10_1 || srcbrctrl10_2 || srcbrctrl11_1 || srcbrctrl11_2 || srcbrctrl12_1 || srcbrctrl12_2 || srcbrctrl13_1 || srcbrctrl13_2 || dstcs10 || dstsd10 || dstcs11 || dstsd11 || dstcs12 || dstsd12 || dstcs13 || dstsd13 || dstbrctrl10_1 || dstbrctrl10_2 || dstbrctrl11_1 || dstbrctrl11_2 || dstbrctrl12_1 || dstbrctrl12_2 || dstbrctrl13_1 || dstbrctrl13_2 {
			if srcSVC || dstSVC {
				log.Debug("Don't notify service applications", "srcHost", srcHost, "IP", srcHost.IP())
				if uint8(np.Qpkt.Act.GetAction()) == 0 || uint8(np.Qpkt.Act.GetAction()) == 1 { //
					np.Qpkt.Mtx.Lock()
					if np.Qpkt.Forward {
						np.Qpkt.Mtx.Unlock()
						qosConfig.Forwarder(np.Qpkt.Rp)
						log.Debug("Control packet forwarded", "id", qp.Rp.Id)
						return
					}
					np.Qpkt.Forward = true
					np.Qpkt.Mtx.Unlock()
					log.Debug("Control packet forwarding enabled", "id", qp.Rp.Id)
					return

				}

				// 			// Release packet if it's action is DROPNOTIFY
				if uint8(np.Qpkt.Act.GetAction()) == 3 {
					np.Qpkt.Rp.Release()
					log.Debug("Control packet released", "id", qp.Rp.Id)
					return
				}
			}
		}
	}

	// log.Debug("Send notification method in router", "packet id", np.Qpkt.Rp.Id, "queue fullness", qosConfig.config.Queues[np.Qpkt.QueueNo].GetFillLevel(), "L4 type", l4hdrType, "l4 hdr", l4hdr)
	log.Debug("Send notification to this packet source", "id", qp.Rp.Id)

	if qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetCongestionWarning().Approach == 0 {
		qosConfig.basicNotifications <- &np
	} else if qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetCongestionWarning().Approach == 2 {
		qosConfig.stochNotifications <- &np
	}
	log.Debug("channel length", "len", len(qosConfig.basicNotifications))

}

func (qosConfig *Configuration) dropPacket(qp *queues.QPkt) {
	// In the case of a DROPNOTIFY action we release the packet in the Notify method
	// after creating the notification packet
	if uint8(qp.Act.GetAction()) == 2 && sendNotification {
		defer qp.Rp.Release()
	}
	if !sendNotification {
		defer qp.Rp.Release()
	} //COMP
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
		Name:              extQueue.Name,
		ID:                extQueue.ID,
		MinBandwidth:      extQueue.MinBandwidth,
		MaxBandWidth:      extQueue.MaxBandWidth,
		PoliceRate:        convStringToNumber(extQueue.PoliceRate),
		MaxLength:         extQueue.MaxLength,
		Priority:          extQueue.Priority,
		CongestionWarning: convertCongestionWarning(extQueue.CongestionWarning),
		Profile:           convertActionProfiles(extQueue.Profile),
	}

	return pq
}

func convertCongestionWarning(externalCW conf.CongestionWarning) queues.CongestionWarning {
	return queues.CongestionWarning{Approach: externalCW.Approach, InformationContent: externalCW.InformationContent}
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
