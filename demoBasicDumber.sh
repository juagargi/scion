#!/bin/bash

#source demoNoAction.sh

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

findISDAS() {
    address=`grep $1 gen/sciond_addresses.json`
    clientISDAS=${address:5:12}
    #return $AS
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
    ./bin/demoappserver -p 400$2 &
    cd $SC
    echo "Set up bwtest server at port 400$2"
    echo ""
}

bwTestClientDumb() {
    clientISDAS=""
    findISDAS $1
    echo $clientISDAS
    SCION_DAEMON_ADDRESS=127.0.0.$1:30255 
    export SCION_DAEMON_ADDRESS 
    cd $GOPATH
    ./bin/demoappclient -s 1-ff00:0:11$2,[127.0.0.1]:400$3 -cs 10,1000,?,9Mbps -sc 10,4,?,1kbps -iter 10 -client $clientISDAS -stopVal 1 -smart 0 &
    local pid=$!
    echo "Set up bwtest client to port 400$3"
    echo ""
    wait $pid
    cd $SC
    firstLine=$(<logs/Demo/bwTestClientTo400$3.txt)
    #echo $firstLine
    if [[ "$firstLine" == *"Fatal error"* ]]; then 
        echo "================================================"
        echo "ERROR: BW Test with server 400$3 not successful"
        echo "================================================"
    fi
    
}

bwTestClientSmart() {
    clientISDAS=""
    findISDAS $1
    echo $clientISDAS
    SCION_DAEMON_ADDRESS=127.0.0.$1:30255 
    export SCION_DAEMON_ADDRESS 
    cd $GOPATH
    ./bin/demoappclient -s 1-ff00:0:11$2,[127.0.0.1]:400$3 -cs 10,1000,?,9Mbps -sc 10,4,?,1kbps -iter 10 -client $clientISDAS -stopVal 1 -smart 1 &
    local pid=$!
    echo "Set up bwtest client to port 400$3"
    echo ""
    wait $pid
    cd $SC
    firstLine=$(<logs/Demo/bwTestClientTo400$3.txt)
    #echo $firstLine
    if [[ "$firstLine" == *"Fatal error"* ]]; then 
        echo "================================================"
        echo "ERROR: BW Test with server 400$3 not successful"
        echo "================================================"
    fi
    
}



./scion.sh stop 

#find gen/ISD1 -name qosConfig.yaml -delete

#cp go/border/qos/testdata/DemoConfigBasic.yaml gen/ISD1/ASff00_0_110/br1-ff00_0_110-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigBasic.yaml gen/ISD1/ASff00_0_110/br1-ff00_0_110-2/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigBasic.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigBasic.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-2/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigBasic.yaml gen/ISD1/ASff00_0_112/br1-ff00_0_112-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigBasic.yaml gen/ISD1/ASff00_0_112/br1-ff00_0_112-2/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_113/br1-ff00_0_113-1/qosConfig.yaml
#cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_113/br1-ff00_0_113-2/qosConfig.yaml

#deleteLogs
killall demoappserver
killall demoappclient

#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.stochNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.stochNotify()+g'} go/border/router.go

for i in $(find gen/ISD1 -name qosConfig.yaml); do
    sed -i -e 's/approach: [0-9]/approach: 0/g' $i
done

#./scion.sh build 




#./build_demo.sh

./scion.sh start nobuild 

echo "Scion started"
echo ""

sleep 3
./supervisor/supervisor.sh status
./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255

#Put last byte of each scionds IP address into IPs array
for i in 0 1 2 3; do
    findIP $i
    IP=$?
    IPs[$i]=$IP
done

#SCMP echo between each AS for 5 packets and put result into respective file
for i in 0 1 2 3; do
    count=0
    for j in ${IPs[*]}; do #20 29 35 44
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

for i in 02 05 08 11; do #2 4 6 8
    bwTestServer 20 $i >> $SC/logs/Demo/bwTestServerAt400$i.txt & #$1:last byte of IP of sciond, $2: port 
done
sleep 2

count=0
for i in 02 05 08; do
    bwTestClientDumb  44 0 $i >> $SC/logs/Demo/bwTestClientTo400$i.txt & #$1: last byte of IP of sciond, $2: AS of server, $3: Port of server
    pids[${count}]=$!
    count=$((count+1))
    sleep 0.3 
done 
bwTestClientSmart  44 0 11 >> $SC/logs/Demo/bwTestClientTo40011.txt &
pids[${count}]=$!

for pid in ${pids[*]}; do
    wait $pid
done
jobs


killall demoappserver
killall demoappclient



./bin/scmp "echo" -remote 1-ff00:0:113,[127.0.0.228] -sciond 127.0.0.20:30255 -c 5  #-local 1-ff00:0:110,[127.0.0.228] 
./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255

./scion.sh stop 
play -q -n synth 0.1 tri  1000.0 
