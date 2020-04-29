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


checkLogs() {
if grep "active=false" logs/br1-ff00_0_110-1.log || grep "intf deactivated" logs/br1-ff00_0_110-1.log
then 
    echo "failed"
    ./scion.sh stop
    exit 1;
fi 
}

deleteLogs
./scion.sh stop 
./scion.sh build 
./scion.sh start nobuild  

#sleep 120

for i in {1..20}; do
    sleep 10 
    checkLogs
    echo "not done yet"
done
echo "succeeded"
./bin/scmp "echo" -remote 1-ff00:0:114,[127.0.0.228] -sciond 127.0.0.19:30255 -c 5
./scion.sh stop
exit 0





