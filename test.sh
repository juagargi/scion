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

singleTime() {
#echo "single Time"
#echo ""
lines=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo400$1.txt | wc -l) #| grep -o "[0-9]*\.*[0-9]*"
#echo $lines
#if [ "$lines" -ne "$2" ]; then 
#    echo "Iteration $2 with server $1 did not end successfully"
#    echo ""
#    return 0
#fi
#echo "yeah"
#echo ""
time$1=$(grep -m$2 'Approximated operation time' logs/Demo/bwTestClientTo400$1.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#echo $thisTime
#return $thisTime
}

add() { n="$@"; bc <<< "${n// /+}"; };

checkEntry() {
    entry02=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | wc -l) #| grep -o "[0-9]*\.*[0-9]*"
#    echo $lines
#    if [ "$lines" -ne "$1" ]; then 
#    echo "Iteration $1 with server $1 did not end successfully"
#    echo ""
#    return 0
#fi
}

addTimes() {
entry02=$(grep 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | wc -l) 
echo $entry02
if [ "$entry02" -ne "$1" ]; then
    time02=0
    echo "Iteration $1 with server 40002 did not end successsfully"
else
    time02=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
fi
#echo $time02

time05=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40005.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#echo $time05

time08=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40008.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#echo $time08

time11=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40011.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#echo $time11

add $time02 $time05 $time08 $time11
echo $alltimes
}

addTimes 1


#temp=$(grep -m3 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
#echo $temp

