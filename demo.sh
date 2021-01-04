#!/bin/bash

add() { n="$@"; bc <<< "${n// /+}"; };

deleteLogs() {
    echo "Delete old logs"
    echo ""
    cd logs 
    rm *.log 
    rm *.OUT
    cd Demo
    rm *.txt
    cd Bsc 
    rm *.txt
    cd ../None
    rm *.txt
    cd ../Stoch
    rm *.txt
    cd $SC
}

pathStatistics() {
output1=`grep "Packets sent" logs/Demo/$1/$2Client0.txt`
output1arr=($output1)
output2=`grep "Packets sent" logs/Demo/$1/$2Client0.txt | tail -n 1`
output2arr=($output2)
path0=${output1arr[@]:2:5} #${output1:25:49}
path1=${output2arr[@]:2:5} #${output2:25:49}
echo "path 0" $path0 >> logs/Demo/control.txt
echo "path 1" $path1 >> logs/Demo/control.txt
echo "" >> logs/Demo/control.txt


#path0client0=$(grep "Packets sent" logs/Demo/$1/$2Client0.txt | head -n 1)
path0client0=$(grep -m 1 "Packets sent" logs/Demo/$1/$2Client0.txt)
#echo $path0client0
path0client0arr=($path0client0)

path1client0=$(grep "Packets sent" logs/Demo/$1/$2Client0.txt | tail -n 1)
#echo $path1client0
path1client0arr=($path1client0)

#path0client1=$(grep "Packets sent" logs/Demo/$1/$2Client1.txt | head -n 1)
path0client1=$(grep -m 1 "Packets sent" logs/Demo/$1/$2Client1.txt)
#echo $path0client1
path0client1arr=($path0client1)

path1client1=$(grep "Packets sent" logs/Demo/$1/$2Client1.txt | tail -n 1)
#echo $path1client1
path1client1arr=($path1client1)

#path0client2=$(grep "Packets sent" logs/Demo/$1/$2Client2.txt | head -n 1)
path0client2=$(grep -m 1 "Packets sent" logs/Demo/$1/$2Client2.txt)
#echo $path0client2
path0client2arr=($path0client2)

path1client2=$(grep "Packets sent" logs/Demo/$1/$2Client2.txt | tail -n 1)
#echo $path1client2
path1client2arr=($path1client2)

#path0client3=$(grep "Packets sent" logs/Demo/$1/$2Client3.txt | head -n 1)
path0client3=$(grep -m 1 "Packets sent" logs/Demo/$1/$2Client3.txt)
#echo $path0client3
path0client3arr=($path0client3)

path1client3=$(grep "Packets sent" logs/Demo/$1/$2Client3.txt | tail -n 1)
#echo $path1client3
path1client3arr=($path1client3)

#path0client4=$(grep "Packets sent" logs/Demo/$1/$2Client4.txt | head -n 1)
path0client4=$(grep -m 1 "Packets sent" logs/Demo/$1/$2Client4.txt)
#echo $path0client4
path0client4arr=($path0client4)

path1client4=$(grep "Packets sent" logs/Demo/$1/$2Client4.txt | tail -n 1)
#echo $path1client4
path1client4arr=($path1client4)

echo ${path0client0arr[@]:2:5} >> logs/Demo/control.txt
if [[ "$path0" == "${path0client0arr[@]:2:5}" ]];then 
    echo "same path" >> logs/Demo/control.txt
    path0client0sent=${path0client0arr[13]}
    path0client0CW=${path0client0arr[16]}
    path1client0sent=${path1client0arr[13]}
    path1client0CW=${path1client0arr[16]}
else 
    echo "different path" >> logs/Demo/control.txt
    path1client0sent=${path0client0arr[13]}
    path1client0CW=${path0client0arr[16]}
    path0client0sent=${path1client0arr[13]}
    path0client0CW=${path1client0arr[16]}
fi

echo ${path0client1arr[@]:2:5} >> logs/Demo/control.txt
if [[ "$path0" == "${path0client1arr[@]:2:5}" ]];then 
    echo "same path" >> logs/Demo/control.txt
    path0client1sent=${path0client1arr[13]}
    path0client1CW=${path0client1arr[16]}
    path1client1sent=${path1client1arr[13]}
    path1client1CW=${path1client1arr[16]}
else 
    echo "different path" >> logs/Demo/control.txt
    path1client1sent=${path0client1arr[13]}
    path1client1CW=${path0client1arr[16]}
    path0client1sent=${path1client1arr[13]}
    path0client1CW=${path1client1arr[16]}
fi

echo ${path0client2arr[@]:2:5} >> logs/Demo/control.txt
if [[ "$path0" == "${path0client2arr[@]:2:5}" ]];then 
    echo "same path" >> logs/Demo/control.txt
    path0client2sent=${path0client2arr[13]}
    path0client2CW=${path0client2arr[16]}
    path1client2sent=${path1client2arr[13]}
    path1client2CW=${path1client2arr[16]}
else 
    echo "different path" >> logs/Demo/control.txt
    path1client2sent=${path0client2arr[13]}
    path1client2CW=${path0client2arr[16]}
    path0client2sent=${path1client2arr[13]}
    path0client2CW=${path1client2arr[16]}
fi

echo ${path0client3arr[@]:2:5} >> logs/Demo/control.txt
if [[ "$path0" == "${path0client3arr[@]:2:5}" ]];then 
    echo "same path" >> logs/Demo/control.txt
    path0client3sent=${path0client3arr[13]}
    path0client3CW=${path0client3arr[16]}
    path1client3sent=${path1client3arr[13]}
    path1client3CW=${path1client3arr[16]}
else 
    echo "different path" >> logs/Demo/control.txt
    path1client3sent=${path0client3arr[13]}
    path1client3CW=${path0client3arr[16]}
    path0client3sent=${path1client3arr[13]}
    path0client3CW=${path1client3arr[16]}
fi

echo ${path0client4arr[@]:2:5} >> logs/Demo/control.txt
if [[ "$path0" == "${path0client4arr[@]:2:5}" ]];then 
    echo "same path" >> logs/Demo/control.txt
    path0client4sent=${path0client4arr[13]}
    path0client4CW=${path0client4arr[16]}
    path1client4sent=${path1client4arr[13]}
    path1client4CW=${path1client4arr[16]}
else 
    echo "different path" >> logs/Demo/control.txt
    path1client4sent=${path0client4arr[13]}
    path1client4CW=${path0client4arr[16]}
    path0client4sent=${path1client4arr[13]}
    path0client4CW=${path1client4arr[16]}
fi


echo "" >> logs/Demo/result.txt
echo "Statistics for path" $path0 ":" >> logs/Demo/result.txt
#echo $path0client0CW $path0client1CW $path0client2CW $path0client3CW $path0client4CW
echo "total amount of packets sent:" $(add $path0client0sent $path0client1sent $path0client2sent $path0client3sent $path0client4sent) >> logs/Demo/result.txt
echo "total amount of CWs received:" $(add $path0client0CW $path0client1CW $path0client2CW $path0client3CW $path0client4CW) >> logs/Demo/result.txt

echo "" >> logs/Demo/result.txt

echo "Statistics for path" $path1 ":" >> logs/Demo/result.txt
#echo $path1client0CW $path1client1CW $path1client2CW $path1client3CW $path1client4CW
echo "total amount of packets sent:" $(add $path1client0sent $path1client1sent $path1client2sent $path1client3sent $path1client4sent) >> logs/Demo/result.txt
echo "total amount of CWs received:" $(add $path1client0CW $path1client1CW $path1client2CW $path1client3CW $path1client4CW) >> logs/Demo/result.txt
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
    echo "=====================about to set up client"
    congestionawareapp -remote 1-ff00:0:11$2,[127.0.0.1]:4000$3 -bw $5Mbps -numberPkts $6 -smrt $4 &
    local pid=$!
    echo "Set up demo client to port 4000$3"
    echo ""
    wait $pid
}


./scion.sh stop >> logs/Demo/control.txt

deleteLogs >> logs/Demo/control.txt

killall congestionawareapp >> logs/Demo/control.txt

sed -i -e 's#qosConfig.SendNotification(qp)#\/\/qosConfig.SendNotification(qp)#g' go/border/qos/qos.go

sed -i -e 's/sendNotification     = true/sendNotification     = false/g' go/border/qos/qos.go
sed -i -e 's/sendNotification = true/sendNotification = false/g' go/border/qos/scheduler/wrrScheduler.go
######## No CWs

./scion.sh start >> logs/Demo/control.txt

echo "Scion started"
echo ""

sleep 1
./supervisor/supervisor.sh status >> logs/Demo/control.txt
./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255 >> logs/Demo/control.txt

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
    echo "Wait until all IFs are active, only $activeIF active" >> logs/Demo/control.txt
    activeIF=$(grep -ow 'active=true' logs/br*.log | wc -l)
    sleep 1
done

echo "All IFs activated"

congestionawareappServer 20 0 >> $SC/logs/Demo/None/ServerAt40000.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 1 >> $SC/logs/Demo/None/ServerAt40001.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 2 >> $SC/logs/Demo/None/ServerAt40002.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 3 >> $SC/logs/Demo/None/ServerAt40003.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 4 >> $SC/logs/Demo/None/ServerAt40004.txt & #$1:last byte of IP of sciond, $2: port 

#done
sleep 1

echo ""
echo "Server started"
echo ""


echo ""
echo "Start 5 Clients that only send traffic, no CWs will be sent"
echo ""

count=0
for i in {0..4}; do
    congestionawareappClient  44 0 $i 0 6 50000 >> $SC/logs/Demo/None/SmartClient$i.txt & #$1: last byte of IP of sciond, $2: AS of server, $3: Port of server
    pids[${count}]=$!
    count=$((count+1))
done 
echo "Started all clients for this run" >> logs/control.txt

for pid in ${pids[*]}; do
    wait $pid
done


echo ""
echo "Execution ended"
echo ""

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with no CWs" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
now="$(date)"
echo $now >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NoneDrops=$(grep -o 'Dropping packet' logs/br*.log | wc -l)
echo $NoneDrops >> logs/Demo/result.txt 
echo "" >> logs/Demo/result.txt

pathStatistics None Smart


./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255 >> logs/Demo/control.txt


./scion.sh stop >> logs/Demo/control.txt













####### Basic Approach

killall congestionawareapp >> logs/Demo/control.txt

for i in $(find gen/ISD1 -name qosConfig.yaml); do
    sed -i -e 's/approach: [0-9]/approach: 0/g' $i
done

sed -i -e 's/sendNotification     = false/sendNotification     = true/g' go/border/qos/qos.go
sed -i -e 's/sendNotification = false/sendNotification = true/g' go/border/qos/scheduler/wrrScheduler.go

sed -i -e 's#\/\/qosConfig.SendNotification(qp)#qosConfig.SendNotification(qp)#g' go/border/qos/qos.go


./scion.sh start >> logs/Demo/control.txt

echo "Scion started"
echo ""

sleep 1
./supervisor/supervisor.sh status >> logs/Demo/control.txt
./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255 >> logs/Demo/control.txt

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
while [ $activeIF -lt 32 ]
do
    echo "Wait until all IFs are active, only $activeIF active" >> logs/Demo/control.txt
    activeIF=$(grep -ow 'active=true' logs/br*.log | wc -l)
    sleep 1
done

echo "All IFs activated"

congestionawareappServer 20 0 >> $SC/logs/Demo/Bsc/ServerAt40000.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 1 >> $SC/logs/Demo/Bsc/ServerAt40001.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 2 >> $SC/logs/Demo/Bsc/ServerAt40002.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 3 >> $SC/logs/Demo/Bsc/ServerAt40003.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 4 >> $SC/logs/Demo/Bsc/ServerAt40004.txt & #$1:last byte of IP of sciond, $2: port 

#done
sleep 1

echo ""
echo "Server started"
echo ""


echo ""
echo "Start 5 Clients that react to congestion warning SCMPs from the basic approach"
echo ""

echo "before starting clients"
count=0
for i in {0..4}; do
    congestionawareappClient  44 0 $i 1 6 50000 >> $SC/logs/Demo/Bsc/SmartClient$i.txt & #$1: last byte of IP of sciond, $2: AS of server, $3: Port of server
    pids[${count}]=$!
    count=$((count+1))
done 
echo "Started all clients for this run" >> logs/control.txt

for pid in ${pids[*]}; do
    wait $pid
done


echo ""
echo "Execution ended"
echo ""

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with basic approach" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
now="$(date)"
echo $now >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NotBasicDrops=$(grep -o 'Dropping packet' logs/br*.log | wc -l)
BasicDrops=$(( $NotBasicDrops - $NoneDrops ))
echo $BasicDrops >> logs/Demo/result.txt 
echo "" >> logs/Demo/result.txt

pathStatistics Bsc Smart


./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255 >> logs/Demo/control.txt


./scion.sh stop >> logs/Demo/control.txt




###### Stoch Approach

killall congestionawareapp >> logs/Demo/control.txt

for i in $(find gen/ISD1 -name qosConfig.yaml); do
    sed -i -e 's/approach: [0-9]/approach: 2/g' $i
done

./scion.sh start nobuild >> logs/Demo/control.txt

echo "Scion started"
echo ""

sleep 1
./supervisor/supervisor.sh status >> logs/Demo/control.txt
./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255 >> logs/Demo/control.txt

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
while [ $activeIF -lt 48 ]
do
    echo "Wait until all IFs are active, only $activeIF active" >> logs/Demo/control.txt
    activeIF=$(grep -ow 'active=true' logs/br*.log | wc -l)
    sleep 1
done

echo "All IFs activated"

congestionawareappServer 20 0 >> $SC/logs/Demo/Stoch/ServerAt40000.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 1 >> $SC/logs/Demo/Stoch/ServerAt40001.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 2 >> $SC/logs/Demo/Stoch/ServerAt40002.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 3 >> $SC/logs/Demo/Stoch/ServerAt40003.txt & #$1:last byte of IP of sciond, $2: port 
congestionawareappServer 20 4 >> $SC/logs/Demo/Stoch/ServerAt40004.txt & #$1:last byte of IP of sciond, $2: port 

sleep 1

echo ""
echo "Server started"
echo ""


echo ""
echo "Start 5 Clients that react to congestion warning SCMPs from the stochastic approach"
echo ""

count=0
for i in {0..4}; do
    congestionawareappClient  44 0 $i 1 6 50000 >> $SC/logs/Demo/Stoch/SmartClient$i.txt & #$1: last byte of IP of sciond, $2: AS of server, $3: Port of server
    pids[${count}]=$!
    count=$((count+1))
done 
echo "Started all clients for this run" >> logs/control.txt

for pid in ${pids[*]}; do
    wait $pid
done

echo ""
echo "Execution ended"
echo ""

echo ""
echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with stochastic approach" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
now="$(date)"
echo $now >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NotStochDrops=$(grep -o 'Dropping packet' logs/br*.log | wc -l)
StochDrops=$(( $NotStochDrops - $NotBasicDrops))
echo $StochDrops >> logs/Demo/result.txt 
echo "" >> logs/Demo/result.txt

pathStatistics Stoch Smart

./bin/showpaths -dstIA 1-ff00:0:110 -sciond 127.0.0.44:30255 >> logs/Demo/control.txt

