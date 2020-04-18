#!/bin/bash

echo "Build scion for the demo"

./scion.sh build 

echo "Scion built"

./supervisor/supervisor.sh shutdown

echo "Supervisor shutdown"

./scion.sh topology -c topology/Demo.topo

echo "Demo topo built"

./supervisor/supervisor.sh &
pid=$!

wait $pid

