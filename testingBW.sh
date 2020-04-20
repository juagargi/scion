#!/bin/bash

deleteLogs() {
    echo "Delete old logs"
    echo ""
    cd logs 
    rm *.log 
    rm *.OUT
    cd Demo
    rm *.txt
    cd $SC
}

findIP() {
    address=`grep 11$1 gen/sciond_addresses.json`
    IP=${address:29:2}
    return $IP
}

findAS() {
    address=`grep $1 gen/sciond_addresses.json`
    AS=${address:16:1}
    return $AS
}

scmpEcho() {
    findAS $2
    sourceAS=$?
    ./bin/scmp "echo" -remote 1-ff00:0:11$1,[127.0.0.228] -sciond 127.0.0.$2:30255 -c 5 >> logs/Demo/echoFrom11${sourceAS}To11$1.txt & #-local 1-ff00:0:11$sourceAS,[127.0.0.228]
    local pid=$!
    wait $pid
    firstLine=$(head -n 1 logs/Demo/echoFrom11${sourceAS}To11$1.txt)
    if [ "$firstLine" != "Using path:" ]; then
        echo "=========================================================="
        echo "ERROR: scmp echo from 11${sourceAS} to 11${1} not successful"
        echo "=========================================================="
    fi
}

bwTestServer() {
    SCION_DAEMON_ADDRESS=127.0.0.$1:30255
    export SCION_DAEMON_ADDRESS 
    cd $GOPATH
    ./bin/bwtestserver -p 4000$2 >> $SC/logs/Demo/bwTestServerAt4000$2.txt &
    cd $SC
    echo "Set up bwtest server at port 4000$2"
    echo ""
}

bwTestClient() {
    SCION_DAEMON_ADDRESS=127.0.0.$1:30255 
    export SCION_DAEMON_ADDRESS 
    cd $GOPATH
    ./bin/bwtestclient -s 1-ff00:0:11$2,[127.0.0.1]:4000$3 -cs 10,1000,?,5Mbps -sc 10,1000,?,4kbps >> $SC/logs/Demo/bwTestClientTo4000$3.txt &
    local pid=$!
    echo "Set up bwtest client to port 4000$3"
    echo ""
    wait $pid
    cd $SC
    firstLine=$(<logs/Demo/bwTestClientTo4000$3.txt)
    #echo $firstLine
    if [[ "$firstLine" == *"Fatal error"* ]]; then #TODO: Seems to be working
        echo "================================================"
        echo "ERROR: BW Test with server 4000$3 not successful"
        echo "================================================"
    fi
    
}

./scion.sh stop 


deleteLogs
killall bwtestserver
killall bwtestclient


#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_110/br1-ff00_0_110-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-2/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-3/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_112/br1-ff00_0_112-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_112/br1-ff00_0_112-2/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfig.yaml gen/ISD1/ASff00_0_113/br1-ff00_0_113-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_113/br1-ff00_0_113-2/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_114/br1-ff00_0_114-1/qosConfig.yaml

#./build_demo.sh

./scion.sh start nobuild 

echo "Scion started"
echo ""

sleep 10
./supervisor/supervisor.sh status

#Put last byte of each scionds IP address into IPs array
for i in 0 1 2 ; do #3 4
    findIP $i
    IP=$?
    IPs[$i]=$IP
done

#SCMP echo between each AS for 5 packets and put result into respective file
for i in 0 1 2 ; do #3 4
    count=0
    for j in ${IPs[*]}; do #19 29 36 44 52
        if [ $count == $i ] ;then 
            count=$((count+1))
            continue
        fi
        count=$((count+1))
        scmpEcho $i $j &
    done
done

wait

echo ""
echo "Scmp echo done"
echo ""

for i in 2 4 6 8; do
    bwTestServer 19 $i & #$1:end of IP of sciond, $2: port 
done
sleep 2

for i in 2 4 6 8; do
    bwTestClient  27 0 $i & #$1: end of IP of sciond, $2: AS of server, $3: Port of server
    pids[${i}]=$!
done 

for pid in ${pids[*]}; do
    wait $pid
done
jobs


killall bwtestserver
killall bwtestclient

./bin/scmp "echo" -remote 1-ff00:0:112,[127.0.0.228] -sciond 127.0.0.19:30255 -c 5  #-local 1-ff00:0:110,[127.0.0.228] 


./scion.sh stop 
