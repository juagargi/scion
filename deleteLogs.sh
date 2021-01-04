#!/bin/bash

echo "Delete old logs"
echo ""
cd logs 
rm *.log 
rm *.OUT
cd Demo
rm *.txt
cd $SC
rm dummy.txt
