#!/bin/bash
#perl -0777 -i -p 's+go func\(\)\{+\/\/go func\(\) \{/g' go/border/router.go
#perl -i s+Start\(\)+//Start\(\)+g router.go
#sed -i s+Start\(\)+//Start\(\)+g go/border/router.go
#sed -i 's+go\sfunc\(\)\s\{+\/\/\sgo\sfunc\(\)\s\{+g' go/border/router.go
#sed -i '{N; s+defer log\.HandlePanic()\n\sr\.bscNotify()+\/\/defer log\.HandlePanic()\n\s\/\/r\.bscNotify()+g}' go/border/router.go
#sed -i -e '{N; s+\}\selse\s\{\nbreak+\}\selses\s\{\nbreak+g}' go/border/router.go
#sed -i '{N; s+hello {}()\n.*world+how \/\/\nare\nyou+g}' go/border/test.txt
#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.bscNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.stochNotify()+g'} go/border/router.go

#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.stochNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.stochNotify()+g'} go/border/router.go
#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.bscNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.bscNotify()+g'} go/border/router.go

#sed -i '{N; s+defer log\.HandlePanic()\n.*r\.bscNotify()+\/\/defer log\.HandlePanic()\n\t\t\/\/r\.bscNotify()+g'} go/border/router.go

#./scion.sh build 

#sed -i '{N; s+\/\/.*defer log\.HandlePanic()\n.*\/\/.*r\.bscNotify()+\t\tdefer log\.HandlePanic()\n\t\tr\.bscNotify()+g}' go/border/router.go


#sed -i '{N; s+\/\/.*defer log\.HandlePanic()\n.*\/\/.*r\.stochNotify()+defer log\.HandlePanic()\n\t\tr\.stochNotify()+g}' go/border/router.go

#sed -i -e '{N; s+\/\/.*defer log\.HandlePanic()\n.*\/\/\s+defer log\.HandlePanic()\n\t\t+g}' go/border/router.go

#for i in $(find gen/ISD1 -name qosConfig.yaml); do
#    sed -i -e 's/approach: [0-9]/approach: 5/g' $i
#done

#Drops=$(grep -ow 'Dropping' logs/br*.log | wc -l) 
#echo $Drops >> logs/Demo/result.txt
#echo $Drops 
#BasicDrops=5
#BasicDrops1=$(( $Drops - $BasicDrops ))
#echo $BasicDrops1

#for i in $(find gen/ISD1 -name qosConfig.yaml); do
#    sed -i -e 's/approach: [0-9]/approach: 2/g' $i
#done

#sed '/^new Demoapp/,/^new Demoapp/p' logs/Demo/bwTestClientTo40002.txt

#totalTime=0
#for i in 02 05 08 11; do
##grep 'Approximated operation time' logs/Demo/bwTestClientTo400$i.txt | awk '/[0-9]+(\.[0-9]+)*/{i++}i==1'
#clientTime=$(awk '/Approximated operation time/{i++}i==1' logs/Demo/bwTestClientTo400$i.txt | grep -o '/[0-9]+(\.[0-9]+)*')
#echo $clientTime
#$totalTime=$(($totalTime+$clientTime))
#done
#echo $totalTime

#awk '/Approximated operation time/{i++}i==1' logs/Demo/bwTestClientTo40005.txt | grep -o '/[0-9]+(\.[0-9]+)*'
#grep '/\d+\.?\d*/' logs/Demo/bwTestClientTo40005.txt
#lines=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | wc -l) #| grep -o "[0-9]*\.*[0-9]*"
#echo $lines
#if [ "$lines" -ne "5" ]; then 
#    echo "fuck"
#fi 
#echo "yeah"
#grep 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt 

#singleTime() {
#echo "single Time"
#echo ""
#lines=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo400$1.txt | wc -l) #| grep -o "[0-9]*\.*[0-9]*"
#echo $lines
#if [ "$lines" -ne "$2" ]; then 
#    echo "Iteration $2 with server $1 did not end successfully"
#    echo ""
#    return 0
#fi
#echo "yeah"
#echo ""
#time$1=$(grep -m$2 'Approximated operation time' logs/Demo/bwTestClientTo400$1.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#echo $thisTime
#return $thisTime
#}

#add() { n="$@"; bc <<< "${n// /+}"; };

#checkEntry() {
 #   entry02=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | wc -l) #| grep -o "[0-9]*\.*[0-9]*"
#    echo $lines
#    if [ "$lines" -ne "$1" ]; then 
#    echo "Iteration $1 with server $1 did not end successfully"
#    echo ""
#    return 0
#fi
#}

#addTimes() {
#entry02=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | wc -l) 
#echo $entry02
#if [ "$entry02" -ne "$1" ]; then
#    time02=0
#    echo "Iteration $1 with server 40002 did not end successsfully"
#else
#    time02=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#fi
##echo $time02
#
#time05=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40005.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
##echo $time05
#
#time08=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40008.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
##echo $time08
#
#time11=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40011.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
##echo $time11
#
#add $time02 $time05 $time08 $time11
#echo $alltimes
#}
#
#addTimes 1
#
#
##temp=$(grep -m3 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
##echo $temp
#
#

#SCION_DAEMON_ADDRESS=127.0.0.20:30255
#export SCION_DAEMON_ADDRESS 
#cd $GOPATH
#./bin/demoappserver -p 40002 




#output=`grep "Packets sent" logs/Demo/Smart/SmartClient1.txt`
#output1=`grep "Packets sent" logs/Demo/Smart/SmartClient1.txt | tail -n 1`
#path=${output:25:49}
#path1=${output1:25:49}
#echo $output
#echo $output1
#echo $path
#echo $path1
#
##packets=`grep "Packets sent" logs/Demo/Smart/SmartClient1.txt` | awk '{print $NF}' #| grep -oP '(?<=received )[0-9]+' #sed 's/.*\CWs received [0-9]*/[0-9]*/'
#echo $packets
#echo `grep "Packets sent" logs/Demo/Smart/SmartClient1.txt` | awk '{print $NF}'


#add() { n="$@"; bc <<< "${n// /+}"; };
#
#
#output1=`grep "Packets sent" logs/Demo/Smart/SmartClient0.txt`
#output1arr=($output1)
#output2=`grep "Packets sent" logs/Demo/Smart/SmartClient0.txt | tail -n 1`
#output2arr=($output2)
#path0=${output1arr[@]:2:5} #${output1:25:49}
#path1=${output2arr[@]:2:5} #${output2:25:49}
#echo "path 0"
#echo $path0
#echo "path 1"
#echo $path1
#echo ""
#
#echo ""
#echo "array approach"
#echo ""
#
#path0client0=$(grep "Packets sent" logs/Demo/Smart/SmartClient0.txt | head -n 1)
##echo $path0client0
#path0client0arr=($path0client0)
#
#path1client0=$(grep "Packets sent" logs/Demo/Smart/SmartClient0.txt | tail -n 1)
##echo $path1client0
#path1client0arr=($path1client0)
#
#path0client1=$(grep "Packets sent" logs/Demo/Smart/SmartClient1.txt | head -n 1)
##echo $path0client1
#path0client1=($path0client1)
#
#path1client1=$(grep "Packets sent" logs/Demo/Smart/SmartClient1.txt | tail -n 1)
##echo $path1client1
#path1client1arr=($path1client1)
#
#path0client2=$(grep "Packets sent" logs/Demo/Smart/SmartClient2.txt | head -n 1)
##echo $path0client2
#path0client2arr=($path0client2)
#
#path1client2=$(grep "Packets sent" logs/Demo/Smart/SmartClient2.txt | tail -n 1)
##echo $path1client2
#path1client2arr=($path1client2)
#
#path0client3=$(grep "Packets sent" logs/Demo/Smart/SmartClient3.txt | head -n 1)
##echo $path0client3
#path0client3arr=($path0client3)
#
#path1client3=$(grep "Packets sent" logs/Demo/Smart/SmartClient3.txt | tail -n 1)
##echo $path1client3
#path1client3arr=($path1client3)
#
#path0client4=$(grep "Packets sent" logs/Demo/Smart/SmartClient4.txt | head -n 1)
##echo $path0client4
#path0client4arr=($path0client4)
#
#path1client4=$(grep "Packets sent" logs/Demo/Smart/SmartClient4.txt | tail -n 1)
##echo $path1client4
#path1client4arr=($path1client4)
#
#if [[ "$path0" == "${path0client0arr[@]:2:5}" ]];then 
#    echo "same path"
#    path0client0sent=${path0client0arr[13]}
#    path0client0CW=${path0client0arr[16]}
#    path1client0sent=${path1client0arr[13]}
#    path1client0CW=${path1client0arr[16]}
#else 
#    echo "different path"
#    path1client0sent=${path0client0arr[13]}
#    path1client0CW=${path0client0arr[16]}
#    path0client0sent=${path1client0arr[13]}
#    path0client0CW=${path1client0arr[16]}
#fi
#
#if [[ "$path0" == "${path0client1arr[@]:2:5}" ]];then 
#    echo "same path"
#    path0client1sent=${path0client1arr[13]}
#    path0client1CW=${path0client1arr[16]}
#    path1client1sent=${path1client1arr[13]}
#    path1client1CW=${path1client1arr[16]}
#else 
#    echo "different path"
#    path1client1sent=${path0client1arr[13]}
#    path1client1CW=${path0client1arr[16]}
#    path0client1sent=${path1client1arr[13]}
#    path0client1CW=${path1client1arr[16]}
#fi
#
#if [[ "$path0" == "${path0client2arr[@]:2:5}" ]];then 
#    echo "same path"
#    path0client2sent=${path0client2arr[13]}
#    path0client2CW=${path0client2arr[16]}
#    path1client2sent=${path1client2arr[13]}
#    path1client2CW=${path1client2arr[16]}
#else 
#    echo "different path"
#    path1client2sent=${path0client2arr[13]}
#    path1client2CW=${path0client2arr[16]}
#    path0client2sent=${path1client2arr[13]}
#    path0client2CW=${path1client2arr[16]}
#fi
#
#if [[ "$path0" == "${path0client3arr[@]:2:5}" ]];then 
#    echo "same path"
#    path0client3sent=${path0client3arr[13]}
#    path0client3CW=${path0client3arr[16]}
#    path1client3sent=${path1client3arr[13]}
#    path1client3CW=${path1client3arr[16]}
#else 
#    echo "different path"
#    path1client3sent=${path0client3arr[13]}
#    path1client3CW=${path0client3arr[16]}
#    path0client3sent=${path1client3arr[13]}
#    path0client3CW=${path1client3arr[16]}
#fi
#
#if [[ "$path0" == "${path0client4arr[@]:2:5}" ]];then 
#    echo "same path"
#    path0client4sent=${path0client4arr[13]}
#    path0client4CW=${path0client4arr[16]}
#    path1client4sent=${path1client4arr[13]}
#    path1client4CW=${path1client4arr[16]}
#else 
#    echo "different path"
#    path1client4sent=${path0client4arr[13]}
#    path1client4CW=${path0client4arr[16]}
#    path0client4sent=${path1client4arr[13]}
#    path0client4CW=${path1client4arr[16]}
#fi
#
#
#
##packetsOnPath0=$(( $path0client0 + $path0client1 + $path0client2 + $path0client3 + $path0client4 ))
##packetsOnPath1=$(( $path1client0 + $path1client1 + $path1client2 + $pat10client3 + $path1client4 ))
#
##echo ${lol[13]}
#echo ""
#echo "Statistics for path" $path0 ":"
#echo "total amount of packets sent:" $(add $path0client0sent $path0client1sent $path0client2sent $path0client3sent $path0client4sent)
#echo "total amount of CWs received:" $(add $path0client0CW $path0client1CW $path0client2CW $path0client3CW $path0client4CW)
#
#echo ""
#
#echo "Statistics for path" $path1 ":"
#echo "total amount of packets sent:" $(add $path1client0sent $path1client1sent $path1client2sent $path1client3sent $path1client4sent)
#echo "total amount of CWs received:" $(add $path1client0CW $path1client1CW $path1client2CW $path1client3CW $path1client4CW)


#sed -i -e 's#qosConfig.SendNotification(qp)#\/\/qosConfig.SendNotification(qp)#g' go/border/qos/qos.go
#sed -i -e 's#\/\/qosConfig.SendNotification(qp)#qosConfig.SendNotification(qp)#g' go/border/qos/qos.go


#sed -i -e 's/sendNotification     = false/sendNotification     = true/g' go/border/qos/qos.go
#sed -i -e 's/sendNotification = false/sendNotification = true/g' go/border/qos/scheduler/wrrScheduler.go
sed -i -e 's#\/\/qosConfig.SendNotification(qp)#qosConfig.SendNotification(qp)#g' go/border/qos/qos.go


#sed -i -e 's#qosConfig.SendNotification(qp)#\/\/qosConfig.SendNotification(qp)#g' go/border/qos/qos.go

