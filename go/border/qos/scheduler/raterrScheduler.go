package scheduler

import (
	"sync"
	"time"

	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/log"
)

// This is also a weighted round robin dequeuer. But instead of the priority
// field it uses the min-bandwidth field for the minimum number of packets to dequeue.
// If there are fewer than the minimal value of packets to dequeue, the remaining min-bandwidth
// will be put onto a surplus counter and another queue might use more than its min-bandwidth
// (but still less than its max-bandwidth).

type RateRoundRobinScheduler struct {
	quantumSum          int
	totalLength         int
	schedulerSurplus    surplus
	schedulerSurplusMtx *sync.Mutex
	messages            chan bool
	jobs                chan int

	sleepDuration int
	cirBuckets    []queues.TokenBucket
	pirBuckets    []queues.TokenBucket
	tb            queues.TokenBucket

	logger ScheduleLogger
}

type surplus struct {
	Surplus    int
	Payments   []int
	MaxSurplus int
}

var _ SchedulerInterface = (*RateRoundRobinScheduler)(nil)

func (sched *RateRoundRobinScheduler) Init(routerConfig *queues.InternalRouterConfig) {

	sched.quantumSum = 0
	sched.totalLength = len(routerConfig.Queues)

	sched.logger = initLogger(sched.totalLength)

	var messageLen int
	for i := 0; i < len(routerConfig.Queues); i++ {
		messageLen += routerConfig.Queues[i].GetCapacity()
	}

	sched.messages = make(chan bool, messageLen)

	sched.schedulerSurplus = surplus{0, make([]int, sched.totalLength), -1}
	sched.schedulerSurplusMtx = &sync.Mutex{}

	sched.jobs = make(chan int, sched.totalLength)

	for i := 0; i < sched.totalLength; i++ {
		sched.quantumSum = sched.quantumSum + routerConfig.Queues[i].GetMinBandwidth()
	}

	maxBW := routerConfig.Scheduler.Bandwidth

	sched.tb.Init(maxBW)
	sched.sleepDuration = routerConfig.Scheduler.Latency

	sched.schedulerSurplus.MaxSurplus = maxBW

	sched.cirBuckets = make([]queues.TokenBucket, sched.totalLength)
	sched.pirBuckets = make([]queues.TokenBucket, sched.totalLength)

	for i := 0; i < sched.totalLength; i++ {
		bw := float64(routerConfig.Queues[i].GetMinBandwidth()) / 100.0
		log.Debug("Init bucket with", "int(maxBW * bw)", int(float64(maxBW)*bw), "bw", bw)
		sched.cirBuckets[i].Init(int(float64(maxBW) * bw))
	}
	for i := 0; i < sched.totalLength; i++ {
		bw := float64(routerConfig.Queues[i].GetMaxBandwidth()) / 100.0
		log.Debug("Init bucket with", "int(maxBW * bw)", int(float64(maxBW)*bw), "bw", bw)
		sched.pirBuckets[i].Init(int(float64(maxBW) * bw))
	}

}

func (sched *RateRoundRobinScheduler) Dequeuer(routerConfig *queues.InternalRouterConfig,
	forwarder func(rp *rpkt.RtrPkt)) {
	if sched.totalLength == 0 {
		panic("There are no queues to dequeue from. Please check that Init is called")
	}
	sleepDuration := time.Duration(time.Duration(sched.sleepDuration) * time.Microsecond)
	for <-sched.messages {
		t0 := time.Now()
		for i := 0; i < sched.totalLength; i++ {
			sched.Dequeue(routerConfig.Queues[i], forwarder, i)
		}
		for i := 0; i < sched.totalLength; i++ {
			_ = <-sched.jobs
		}
		sched.showLog(*routerConfig)

		for time.Now().Sub(t0) < sleepDuration {
			time.Sleep(time.Duration(sched.sleepDuration/10) * time.Microsecond)
		}
	}
}

func (sched *RateRoundRobinScheduler) showLog(routerConfig queues.InternalRouterConfig) {

	sched.logger.iterations++
	if time.Now().Sub(sched.logger.t0) > time.Duration(10*time.Second) {

		var queLen = make([]int, sched.totalLength)
		for i := 0; i < sched.totalLength; i++ {
			queLen[i] = routerConfig.Queues[i].GetLength()
		}
		log.Debug("STAT", "iterations", sched.logger.iterations,
			"incoming", sched.logger.incoming,
			"deqLastRound", sched.logger.lastRound,
			"deqAttempted", sched.logger.attempted,
			"deqTotal", sched.logger.total,
			"currQueueLen", queLen,
			"surplus", sched.schedulerSurplus.Surplus)

		if len(sched.cirBuckets) > 3 {
			log.Debug("STAT Available cirTokens",
				"1", sched.cirBuckets[1].GetAvailable(),
				"2", sched.cirBuckets[2].GetAvailable(),
				"3", sched.cirBuckets[3].GetAvailable())
			log.Debug("STAT Available pirTokens",
				"1", sched.pirBuckets[1].GetAvailable(),
				"2", sched.pirBuckets[2].GetAvailable(),
				"3", sched.pirBuckets[3].GetAvailable())
			log.Debug("STAT",
				"tokensUsed", sched.logger.tokensUsed,
				"forceTake", sched.logger.forceTake,
				"cirTokens", sched.logger.cirTokens,
				"pirTokens", sched.logger.pirTokens,
				"payedIntoSurplus", sched.logger.payedIntoSurplus)
			amount0 := float64(sched.logger.tokensUsed[0] + sched.logger.forceTake[0])
			amount1 := float64(sched.logger.tokensUsed[1] + sched.logger.forceTake[1])
			amount2 := float64(sched.logger.tokensUsed[2] + sched.logger.forceTake[2])
			amount3 := float64(sched.logger.tokensUsed[3] + sched.logger.forceTake[3])
			amount4 := float64(sched.logger.tokensUsed[4] + sched.logger.forceTake[4])
			queue0 := float64(amount0) / 5.0 / 1000000.0 * 8.0
			queue1 := float64(amount1) / 5.0 / 1000000.0 * 8.0
			queue2 := float64(amount2) / 5.0 / 1000000.0 * 8.0
			queue3 := float64(amount3) / 5.0 / 1000000.0 * 8.0
			queue4 := float64(amount4) / 5.0 / 1000000.0 * 8.0
			overall := float64(sched.logger.overallTokensUsed) / 5.0 / 1000000.0
			log.Debug("STAT",
				"overall", overall,
				"maxOverall", 2,
				"0", queue0,
				"1", queue1,
				"2", queue2,
				"3", queue3,
				"4", queue4)

		}
		for i := 0; i < len(sched.logger.lastRound); i++ {
			sched.logger.lastRound[i] = 0
		}
		for i := 0; i < len(sched.logger.attempted); i++ {

			sched.logger.attempted[i] = 0
		}
		for i := 0; i < len(sched.logger.incoming); i++ {
			sched.logger.incoming[i] = 0
		}
		for i := 0; i < len(sched.logger.tokensUsed); i++ {
			sched.logger.tokensUsed[i] = 0
		}
		for i := 0; i < len(sched.logger.cirTokens); i++ {
			sched.logger.cirTokens[i] = 0
		}
		for i := 0; i < len(sched.logger.pirTokens); i++ {
			sched.logger.pirTokens[i] = 0
		}
		for i := 0; i < len(sched.logger.payedIntoSurplus); i++ {
			sched.logger.payedIntoSurplus[i] = 0
		}
		for i := 0; i < len(sched.logger.forceTake); i++ {
			sched.logger.forceTake[i] = 0
		}
		sched.logger.overallTokensUsed = 0
		sched.logger.t0 = time.Now()
		sched.logger.iterations = 0
	}

}

var pktLen int

func (sched *RateRoundRobinScheduler) Dequeue(queue queues.PacketQueueInterface,
	forwarder func(rp *rpkt.RtrPkt), queueNo int) {
	no := 5
	sched.logger.attempted[queueNo] += no
	sched.dequeuePackets(queue, no, forwarder, queueNo)
}

func (sched *RateRoundRobinScheduler) dequeuePackets(queue queues.PacketQueueInterface,
	pktToDequeue int, forwarder func(rp *rpkt.RtrPkt), queueNo int) int {
	var qp *queues.QPkt
	j := 0

	for i := 0; i < pktToDequeue; i++ {
		if !sched.availableFromBuckets(1500, queueNo) {
			break
		}
		qp = queue.Pop()
		if qp == nil {
			break
		}
		j++

		pktLen = len(qp.Rp.Raw)

		if !(sched.takeFromBuckets(pktLen, queueNo)) {

			sched.cirBuckets[queueNo].ForceTake(pktLen)

			sched.pirBuckets[queueNo].ForceTake(pktLen)
			forwarder(qp.Rp)
			break
		}
		forwarder(qp.Rp)
	}

	sched.logger.lastRound[queueNo] += j
	sched.logger.total[queueNo] += j
	sched.jobs <- j
	for i := 0; i < 5; i++ {
		if sched.cirBuckets[queueNo].Take(1500) {
			sched.logger.payedIntoSurplus[queueNo] += 1500
			sched.payIntoSurplus(queue, queueNo, 1500)
		}
	}
	return j
}

func (sched *RateRoundRobinScheduler) availableFromBuckets(packetLength int, queueNo int) bool {
	if sched.tb.Available(packetLength) {
		if sched.cirBuckets[queueNo].Available(packetLength) {
			return true
		}
		if sched.pirBuckets[queueNo].Available(packetLength) {
			if sched.availableSurplus(packetLength) {
				return true
			}
		}
	}
	return false
}

func (sched *RateRoundRobinScheduler) takeFromBuckets(packetLength int, queueNo int) bool {

	if sched.tb.Available(packetLength) {

		if sched.cirBuckets[queueNo].Take(packetLength) {
			sched.pirBuckets[queueNo].ForceTake(packetLength)
			sched.tb.ForceTake(packetLength)
			sched.logger.overallTokensUsed += packetLength
			sched.logger.tokensUsed[queueNo] += packetLength
			sched.logger.cirTokens[queueNo] += packetLength
			return true
		}

		if sched.pirBuckets[queueNo].Available(packetLength) {
			if sched.takeSurplus(packetLength) {
				sched.pirBuckets[queueNo].Take(packetLength)
				sched.tb.ForceTake(packetLength)
				sched.logger.overallTokensUsed += packetLength
				sched.logger.tokensUsed[queueNo] += packetLength
				sched.logger.pirTokens[queueNo] += packetLength
				return true
			}
		}
	}

	sched.logger.overallTokensUsed += packetLength
	sched.logger.forceTake[queueNo] += packetLength

	return false
}
func (sched *RateRoundRobinScheduler) availableSurplus(amount int) bool {

	if sched.schedulerSurplus.Surplus > amount {
		return true
	}
	return false
}

func (sched *RateRoundRobinScheduler) takeSurplus(amount int) bool {
	if sched.schedulerSurplus.Surplus > amount {
		sched.schedulerSurplus.Surplus -= amount
		return true
	}
	return false
}

func (sched *RateRoundRobinScheduler) payIntoSurplus(queue queues.PacketQueueInterface,
	queueNo int, payment int) {

	a := sched.schedulerSurplus.Surplus + payment
	b := sched.schedulerSurplus.MaxSurplus
	sched.schedulerSurplus.Surplus = min(a, b)
	sched.schedulerSurplus.Payments[queueNo] = sched.schedulerSurplus.Surplus
}

func (sched *RateRoundRobinScheduler) GetMessages() *chan bool {
	return &sched.messages
}
