package scheduler

import (
	"fmt"
	"testing"
	"time"
)

func TestNoPackets(t *testing.T) {
	var testTable = []struct {
		length         int
		priority       int
		prioritySum    int
		expectedResult int
	}{
		{100, 1, 10, 1},
		{100, 2, 10, 2},
		{100, 3, 10, 3},
		{100, 4, 10, 4},
		{100, 5, 10, 5},
		{100, 6, 10, 6},
		{100, 7, 10, 7},
		{100, 8, 10, 8},
		{100, 9, 10, 9},
		{100, 10, 10, 10},
		{100, 1, 11, 1},
		{100, 8, 11, 8},
		{100, 2, 11, 2},
	}
	for _, test := range testTable {
		result := getNoPacketsToDequeue(test.length, test.priority, test.prioritySum)
		if result != test.expectedResult {
			t.Errorf("Wanted %d got %d", test.expectedResult, result)
		}

	}
}

var simTable = []struct {
	priority           int
	maxLength          int
	length             int
	lastPeriodDequeued int
	lastPeriodArrived  int
	totalDequeued      int
	totalArrived       int
}{
	{1, 1024, 0, 0, 0, 0, 0},
	{8, 1024, 0, 0, 0, 0, 0},
	{2, 1024, 0, 0, 0, 0, 0},
}

func updateTable(queueNo int, removed int, arrived int) {

	simTable[queueNo].length -= removed
	simTable[queueNo].length += arrived

	simTable[queueNo].lastPeriodDequeued = removed
	simTable[queueNo].lastPeriodArrived = arrived

	simTable[queueNo].totalDequeued += removed
	simTable[queueNo].totalArrived += arrived
}

func printTable(step int) {
	fmt.Print(step)
	for i := 0; i < 3; i++ {
		fmt.Printf(" Queue %d len %d deq %d arr %d;",
			i,
			simTable[i].length,
			simTable[i].lastPeriodDequeued,
			simTable[i].lastPeriodArrived)
	}
	fmt.Print("\n")
}

func TestSimtable(t *testing.T) {
	for i := 0; i < 1000; i++ {
		totLen := simTable[0].length + simTable[1].length + simTable[2].length
		updateTable(0, getNoPacketsToDequeue(totLen, 1, 11), 10)
		updateTable(1, getNoPacketsToDequeue(totLen, 8, 11), 100)
		updateTable(2, getNoPacketsToDequeue(totLen, 2, 11), 100)

		time.Sleep(800 * time.Microsecond)
		printTable(i)
	}

	fmt.Println("Queue 0", simTable[0].totalDequeued,
		"Queue 1", simTable[1].totalDequeued,
		"Queue 2", simTable[2].totalDequeued)

	// t.Errorf("Show log")
}
