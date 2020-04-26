package scheduler

import (
	"log"
	"math"
	"os"
	"runtime/pprof"
	"sync"
	"testing"

	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
)

func TestEnAndDequeuePackets(T *testing.T) {

	f, err := os.Create("cpu.out")
	if err != nil {
		log.Fatal(err)
	}
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	qp := queues.QPkt{Rp: nil, QueueNo: 0}

	queue1 := queues.ChannelPacketQueue{}
	queue1.InitQueue(
		queues.PacketQueue{
			MaxLength:    1024,
			MinBandwidth: 30,
			MaxBandWidth: 40},
		&sync.Mutex{},
		&sync.Mutex{})

	queue2 := queues.ChannelPacketQueue{}
	queue2.InitQueue(
		queues.PacketQueue{
			MaxLength:    1024,
			MinBandwidth: 60,
			MaxBandWidth: 80},
		&sync.Mutex{},
		&sync.Mutex{})

	mockSched := &RateRoundRobinScheduler{}
	mockSched.Init(
		&queues.InternalRouterConfig{
			Queues: []queues.PacketQueueInterface{
				&queue1,
				&queue2}})

	j := 100

	for n := 0; n < int(math.Pow10(4)); n++ {

		for i := 0; i < j; i++ {
			queue1.Enqueue(&qp)
		}
		j = mockSched.dequeuePackets(&queue1, 100, forwardPacketByDrop, 0)
		j = <-mockSched.jobs
		// fmt.Println("Dequeued", j)
		// fmt.Println("Iteration", n)
	}
}

var testQueue = make(chan int, 1000)
var blockForwarder = make(chan int, 1)

func forwardPacketByDrop(rp *rpkt.RtrPkt) {
	// testQueue <- 0
	// rp.Release()
}

func forwardPacketByDropAndWait(rp *rpkt.RtrPkt) {
	<-blockForwarder
	testQueue <- 0
	rp.Release()
}
