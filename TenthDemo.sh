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

congestionawareappServer() {
    SCION_DAEMON_ADDRESS=127.0.0.$1:30255
    export SCION_DAEMON_ADDRESS 
    congestionawareapp -port 4000$2 &
    echo "Set up congestion aware application server at port 4000$2"
    echo ""
}

congestionawareappClient() {
    clientISDAS=""
    findISDAS $1
    echo $clientISDAS
    SCION_DAEMON_ADDRESS=127.0.0.$1:30255 
    export SCION_DAEMON_ADDRESS 
    congestionawareapp -remote 1-ff00:0:11$2,[127.0.0.1]:4000$3 -bw 8Mbps -numberPkts 50000 -smrt 1 &
    local pid=$!
    echo "Set up demo client to port 4000$3"
    echo ""
    wait $pid
}


./scion.sh stop

deleteLogs
killall congestionawareapp


for i in $(find gen/ISD1 -name qosConfig.yaml); do
    sed -i -e 's/approach: [0-9]/approach: 0/g' $i
done

./scion.sh start nobuild 

echo "Scion started"
echo ""

sleep 5
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

activeIF=$(grep -ow 'active=true' logs/br*.log | wc -l)
while [ $activeIF -lt 16 ]
do
    echo "Wait until all IFs are active, only $activeIF active"
    activeIF=$(grep -ow 'active=true' logs/br*.log | wc -l)
    sleep 1
done

echo "All IFs activated"

#for i in {0..1}; do 
congestionawareappServer 20 2 >> $SC/logs/Demo/ServerAt40002.txt & #$1:last byte of IP of sciond, $2: port 
#done
sleep 1

count=0
for i in {0..4}; do
    congestionawareappClient  44 0 2 >> $SC/logs/Demo/Client$i.txt & #$1: last byte of IP of sciond, $2: AS of server, $3: Port of server
    pids[${count}]=$!
    count=$((count+1))
done 

for pid in ${pids[*]}; do
    wait $pid
done

killall congestionawareapp


for i in {0..4}; do
    if grep -q "Received SCMP revocation header" logs/Demo/Client$i.txt
    then 
        echo "client to 4000$i received  IF revocation"
        exit 1
    fi
done 



./bin/scmp "echo" -remote 1-ff00:0:113,[127.0.0.228] -sciond 127.0.0.20:30255 -c 5  #-local 1-ff00:0:110,[127.0.0.228] 
./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255

./scion.sh stop 
#play -q -n synth 0.1 tri  1000.0 
