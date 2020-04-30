#!/bin/bash

./deleteLogs.sh

for i in {1..10};do
    ./basic_bwtest.sh >> dummy.txt
    echo "$i"
    echo ""
done
play -q -n synth 0.1 tri  1000.0 
