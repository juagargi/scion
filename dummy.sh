#!/bin/bash

./deleteLogs.sh

for i in {1..10};do
    ./demo.sh >> dummy.txt
    echo "$i"
    echo ""
done
#play -q -n synth 0.1 tri  1000.0 
