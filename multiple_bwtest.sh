#!/bin/bash

./scion.sh stop 

echo "Delete old logs"
cd logs 
rm *.log 
rm *.OUT

cd $SC

./scion.sh start nobuild 

echo "Scion started"

wait 2

./bin/scmp "echo" -remote 1-ff00:0:110,[127.0.0.228] -sciond 127.0.0.52:30255 -c 5 


./bin/scmp "echo" -remote 1-ff00:0:110,[127.0.0.228] -sciond 127.0.0.52:30255 -c 5 


echo "finished scmp echo"

SCION_DAEMON_ADDRESS='127.0.0.20:30255'
export SCION_DAEMON_ADDRESS 
#for i in 02 04 06 08 10
#do
#    scion-bwtestserver -p 400$i &
#    pid${i}=$!
#done

cd $GOPATH

./bin/bwtestserver -p 40000 &
pid0=$!

./bin/bwtestserver -p 40002 &
pid2=$!

./bin/bwtestserver -p 40004 &
pid4=$!

./bin/bwtestserver -p 40006 &
pid6=$!

./bin/bwtestserver -p 40008 &
pid8=$!

cd $SC

jobs
echo "set up bwtest server"

wait 2

echo "about to set up bwtest client"

cd $GOPATH

SCION_DAEMON_ADDRESS='127.0.0.44:30255' 
export SCION_DAEMON_ADDRESS 
./bin/bwtestclient -s 1-ff00:0:110,[127.0.0.1]:40000 -cs 10,1000,?,5Mbps -sc 0,0,?,1Mbps & >> logs/Demo/test.txt
pid1=$!

./bin/bwtestclient -s 1-ff00:0:110,[127.0.0.1]:40002 -cs 10,1000,?,5Mbps -sc 0,0,?,1Mbps &
pid3=$!

./bin/bwtestclient -s 1-ff00:0:110,[127.0.0.1]:40004 -cs 10,1000,?,5Mbps -sc 0,0,?,1Mbps &
pid5=$!

./bin/bwtestclient -s 1-ff00:0:110,[127.0.0.1]:40006 -cs 10,1000,?,5Mbps -sc 0,0,?,1Mbps &
pid7=$!

./bin/bwtestclient -s 1-ff00:0:110,[127.0.0.1]:40008 -cs 10,1000,?,5Mbps -sc 0,0,?,1Mbps &
pid9=$!

cd $SC

echo "set up bwtest clients"

wait $pid1 
wait $pid3
wait $pid5
wait $pid7
wait $pid9

echo "about to end processes"

#for i in 02 04 06 08 10
#do
#    kill -9 $pid${i}
#done
kill -9 $pid0 
kill -9 $pid2
kill -9 $pid4
kill -9 $pid6
kill -9 $pid8

wait $pid0
wait $pid2
wait $pid4
wait $pid6
wait $pid8
jobs
./scion.sh stop 
