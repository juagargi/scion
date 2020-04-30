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

cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_110/br1-ff00_0_110-1/qosConfig.yaml
cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_110/br1-ff00_0_110-2/qosConfig.yaml
cp go/border/qos/testdata/DemoConfig.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-1/qosConfig.yaml
cp go/border/qos/testdata/DemoConfig.yaml gen/ISD1/ASff00_0_111/br1-ff00_0_111-2/qosConfig.yaml
cp go/border/qos/testdata/DemoConfig.yaml gen/ISD1/ASff00_0_112/br1-ff00_0_112-1/qosConfig.yaml
cp go/border/qos/testdata/DemoConfig.yaml gen/ISD1/ASff00_0_112/br1-ff00_0_112-2/qosConfig.yaml
cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_113/br1-ff00_0_113-1/qosConfig.yaml
cp go/border/qos/testdata/DemoConfigEmpty.yaml gen/ISD1/ASff00_0_113/br1-ff00_0_113-2/qosConfig.yaml