rm demo.txt

add() { n="$@"; bc <<< "${n// /+}"; };

addTimes() {
time02=$(grep -m$1 'Approximated operation time' logs/Demo/bwTestClientTo40002.txt | grep -o "[0-9]*\.*[0-9]*" | tail -n1)
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



./deleteLogs.sh


echo "================================================" >> demo.txt
echo "Run without congestion warning messages" >> demo.txt
echo "================================================" >> demo.txt
./demoNoAction.sh >> demo.txt

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run without congestion warning messages" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NoActionDrops=$(grep -ow 'Dropping' logs/br*.log | wc -l)
echo $NoActionDrops >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Amount of SCMP congestion warning messages:" >> logs/Demo/result.txt
NoActionCWs=$(grep -ow 'Notification' logs/br*.log | wc -l)
echo $NoActionCWs >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Approximated operation time:" >> logs/Demo/result.txt
Times=$(addTimes 1)
echo $Times >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt



echo "================================================" >> demo.txt
echo "Run with basic congestion warning messages and smart hosts" >> demo.txt
echo "================================================" >> demo.txt
./demoBasic.sh >> demo.txt

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with basic congestion warning messages and smart hosts" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NotBasicDrops=$(grep -ow 'Dropping' logs/br*.log | wc -l) 
BasicDrops=$(( $NotBasicDrops - $NoActionDrops ))
echo $BasicDrops >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Amount of SCMP congestion warning messages:" >> logs/Demo/result.txt
NotBasicCWs=$(grep -ow 'Notification' logs/br*.log | wc -l)
BasicCWs=$(( $NotBasicCWs - $NoActionCWs ))
echo $BasicCWs >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Approximated operation time:" >> logs/Demo/result.txt
Times=$(addTimes 2)
echo $Times >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt


echo "================================================" >> demo.txt
echo "Run with basic congestion warning messages and mostly dumb hosts" >> demo.txt
echo "================================================" >> demo.txt
./demoBasicDumber.sh >> demo.txt

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with basic congestion warning messages and mostly dumb hosts" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NotBasicDumbDrops=$(grep -ow 'Dropping' logs/br*.log | wc -l) 
BasicDumbDrops=$(( $NotBasicDumbDrops - $BasicDrops - $NoActionDrops ))
echo $BasicDumbDrops >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Amount of SCMP congestion warning messages:" >> logs/Demo/result.txt
NotBasicDumbCWs=$(grep -ow 'Notification' logs/br*.log | wc -l)
BasicDumbCWs=$(( $NotBasicDumbCWs - $BasicCWs - $NoActionCWs ))
echo $BasicDumbCWs >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Approximated operation time:" >> logs/Demo/result.txt
Times=$(addTimes 3)
echo $Times >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt


echo "================================================" >> demo.txt
echo "Run with stochastic congestion warning messages and smart hosts" >> demo.txt
echo "================================================" >> demo.txt
./demoStoch.sh >> demo.txt

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with stochastic congestion warning messages and smart hosts" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NotStochDrops=$(grep -ow 'Dropping' logs/br*.log | wc -l) 
StochDrops=$(( $NotStochDrops - $BasicDumbDrops - $BasicDrops -$NoActionDrops ))
echo $StochDrops >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Amount of SCMP congestion warning messages:" >> logs/Demo/result.txt
NotStochCWs=$(grep -ow 'Notification' logs/br*.log | wc -l)
StochCWs=$(( $NotStochCWs - $BasicDumbCWs - $NotBasicCWs - $NoActionCWs ))
echo $StochCWs >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Approximated operation time:" >> logs/Demo/result.txt
Times=$(addTimes 4)
echo $Times >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt



echo "================================================" >> demo.txt
echo "Run with stochastic congestion warning messages and mostly dumb hosts" >> demo.txt
echo "================================================" >> demo.txt
./demoStochDumb.sh >> demo.txt

echo "==============================================================" >> logs/Demo/result.txt
echo "Results from run with stochastic congestion warning messages and mustly dumb hosts" >> logs/Demo/result.txt
echo "==============================================================" >> logs/Demo/result.txt
echo "Amount of packets dropped:" >> logs/Demo/result.txt
NotStochDumbDrops=$(grep -ow 'Dropping' logs/br*.log | wc -l) 
StochDumbDrops=$(( $NotStochDumbDrops - $StochDrops - $BasicDumbDrops - $BasicDrops -$NoActionDrops ))
echo $StochDumbDrops >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Amount of SCMP congestion warning messages:" >> logs/Demo/result.txt
NotStochDumbCWs=$(grep -ow 'Notification' logs/br*.log | wc -l)
StochDumbCWs=$(( $NotStochDumbCWs - $StochCWs - $BasicDumbCWs - $NotBasicCWs - $NoActionCWs ))
echo $StochDumbCWs >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
echo "Approximated operation time:" >> logs/Demo/result.txt
Times=$(addTimes 5)
echo $Times >> logs/Demo/result.txt
echo "" >> logs/Demo/result.txt
