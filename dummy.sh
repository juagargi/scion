#!/bin/bash

./deleteLogs.sh

for i in {1..20};do
    ./basic_bwtest.sh >> dummy.txt
done