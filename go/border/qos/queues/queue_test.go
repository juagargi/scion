package queues_test

import (
	"sync"
	"testing"

	"github.com/scionproto/scion/go/border/qos/queues"
)

func BenchmarkQueuesPopMult(b *testing.B) {

	queueImplementations := []struct {
		name       string
		queueToUse queues.PacketQueueInterface
	}{
		{"Channel Packet Queue", &queues.ChannelPacketQueue{}},
		{"Buffer Packet Queue", &queues.PacketBufQueue{}},
		{"Custom Packet Queue", &queues.CustomPacketQueue{}},
		{"Slice Packet Queue", &queues.PacketSliceQueue{}},
	}

	for _, qi := range queueImplementations {
		var queueBenchmarks = []struct {
			name       string
			count      int
			maxLength  int
			queueToUse queues.PacketQueueInterface
		}{
			{"Bench Queue PopMult 10 len 10 " + qi.name, 10, 10, qi.queueToUse},
			{"Bench Queue PopMult 10 len 64 " + qi.name, 10, 64, qi.queueToUse},
			{"Bench Queue PopMult 10 len 512 " + qi.name, 10, 512, qi.queueToUse},
			{"Bench Queue PopMult 10 len 1024 " + qi.name, 10, 1024, qi.queueToUse},
			{"Bench Queue PopMult 64 len 64 " + qi.name, 64, 64, qi.queueToUse},
			{"Bench Queue PopMult 64 len 512 " + qi.name, 64, 512, qi.queueToUse},
			{"Bench Queue PopMult 64 len 1024 " + qi.name, 64, 1024, qi.queueToUse},
			{"Bench Queue PopMult 512 len 512 " + qi.name, 512, 512, qi.queueToUse},
			{"Bench Queue PopMult 512 len 1024 " + qi.name, 512, 1024, qi.queueToUse},
			{"Bench Queue PopMult 1024 len 1024 " + qi.name, 1024, 1024, qi.queueToUse},
		}
		for _, bench := range queueBenchmarks {
			b.Run(bench.name, func(b *testing.B) {
				benchQueue(b, bench.count, bench.maxLength, bench.queueToUse)
			})
		}
	}
}

func BenchmarkQueuesPopSingle(b *testing.B) {

	queueImplementations := []struct {
		name       string
		queueToUse queues.PacketQueueInterface
	}{
		{"Channel Packet Queue", &queues.ChannelPacketQueue{}},
		{"Buffer Packet Queue", &queues.PacketBufQueue{}},
		{"Custom Packet Queue", &queues.CustomPacketQueue{}},
		{"Slice Packet Queue", &queues.PacketSliceQueue{}},
	}

	for _, qi := range queueImplementations {
		var queueBenchmarks = []struct {
			name       string
			count      int
			maxLength  int
			queueToUse queues.PacketQueueInterface
		}{
			{"Bench Queue 1 len 10 " + qi.name, 1, 10, qi.queueToUse},
			{"Bench Queue 1 len 64 " + qi.name, 1, 64, qi.queueToUse},
			{"Bench Queue 1 len 256 " + qi.name, 1, 256, qi.queueToUse},
			{"Bench Queue 1 len 512 " + qi.name, 1, 512, qi.queueToUse},
			{"Bench Queue 1 len 1024 " + qi.name, 1, 1024, qi.queueToUse},
			{"Bench Queue 2 len 10 " + qi.name, 2, 10, qi.queueToUse},
			{"Bench Queue 2 len 64 " + qi.name, 2, 64, qi.queueToUse},
			{"Bench Queue 2 len 256 " + qi.name, 2, 256, qi.queueToUse},
			{"Bench Queue 2 len 512 " + qi.name, 2, 512, qi.queueToUse},
			{"Bench Queue 2 len 1024 " + qi.name, 2, 1024, qi.queueToUse},
			{"Bench Queue 10 len 10 " + qi.name, 10, 10, qi.queueToUse},
			{"Bench Queue 10 len 64 " + qi.name, 10, 64, qi.queueToUse},
			{"Bench Queue 10 len 256 " + qi.name, 10, 256, qi.queueToUse},
			{"Bench Queue 10 len 512 " + qi.name, 10, 512, qi.queueToUse},
			{"Bench Queue 10 len 1024 " + qi.name, 10, 1024, qi.queueToUse},
			{"Bench Queue 64 len 64 " + qi.name, 64, 64, qi.queueToUse},
			{"Bench Queue 64 len 256 " + qi.name, 64, 256, qi.queueToUse},
			{"Bench Queue 64 len 512 " + qi.name, 64, 512, qi.queueToUse},
			{"Bench Queue 64 len 1024 " + qi.name, 64, 1024, qi.queueToUse},
		}
		for _, bench := range queueBenchmarks {
			b.Run(bench.name, func(b *testing.B) {
				benchQueue(b, bench.count, bench.maxLength, bench.queueToUse)
			})
		}
	}
}

func benchQueue(b *testing.B, count, maxLength int, queueToUse queues.PacketQueueInterface) {

	muta := &sync.Mutex{}
	mutb := &sync.Mutex{}

	intQue := queues.PacketQueue{
		Name:         "Channel Packet Queue",
		ID:           0,
		MinBandwidth: 0,
		MaxBandWidth: 0,
		PoliceRate:   0,
		MaxLength:    maxLength,
		Priority:     100,
		Profile:      nil}

	queueToUse.InitQueue(intQue, muta, mutb)

	qp := &queues.QPkt{Rp: nil, QueueNo: 0}
	var retPkt *queues.QPkt

	for n := 0; n < b.N; n++ {
		for i := 0; i < count; i++ {
			queueToUse.Enqueue(qp)
		}
		for i := 0; i < count; i++ {
			retPkt = queueToUse.Pop()
			_ = retPkt
		}
	}
}

func benchQueuePopMulti(b *testing.B, count, maxLength int, queueToUse queues.PacketQueueInterface) {

	muta := &sync.Mutex{}
	mutb := &sync.Mutex{}

	intQue := queues.PacketQueue{
		Name:         "Channel Packet Queue",
		ID:           0,
		MinBandwidth: 0,
		MaxBandWidth: 0,
		PoliceRate:   0,
		MaxLength:    maxLength,
		Priority:     100,
		Profile:      nil}

	queueToUse.InitQueue(intQue, muta, mutb)

	qp := &queues.QPkt{Rp: nil, QueueNo: 0}
	var retPkt []*queues.QPkt

	for n := 0; n < b.N; n++ {
		for i := 0; i < count; i++ {
			queueToUse.Enqueue(qp)
		}
		retPkt = queueToUse.PopMultiple(count)
		_ = retPkt
	}
}
