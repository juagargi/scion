#!/usr/bin/python3
# Copyright 2016 ETH Zurich
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import os
import sys
import subprocess
import json
import copy
import yaml
import time
import re
import math


SC = os.environ['SC']
# example output:
# Speed: 1.295 Mbps drop rate: 0.000000
# groups: (speed) (units) (drop_rate)
CLIENT_REGEX = re.compile(r'^Speed: (\S+) (\S+) drop rate: (\d*\.\d+|\d+)$')

def bw_class_to_bw(bwClass):
    'bps = 16×(√2^(c-1))×1000'
    'Returns the BW in bytes per second'
    return (2**(bwClass - 1))**(1/2) * 16 * 1000

def split_to_proportion(split):
    '√2^(−s)'
    return (2**(-split))**(1/2)

def get_bw(bwClass, split):
    return math.floor(bw_class_to_bw(bwClass) * (1.0 - split_to_proportion(split)))


def check_output(cmd):
    try:
        output = subprocess.check_output(cmd, cwd=SC, stderr=subprocess.STDOUT)
    except subprocess.CalledProcessError as ex:
        print('{}\nProcess exited non-zero: {}\nIts output is:\n'.format(' '.join(cmd), ex.returncode))
        print(ex.output.decode())
        sys.exit(1)
    return output.decode()

def clean_gen_cache():
    gen_cache = os.path.join(SC, 'gen-cache')
    for f in os.listdir(gen_cache):
        os.unlink(os.path.join(gen_cache, f))

def gen_vanilla():
    clean_gen_cache()
    check_output( ('./scion.sh', 'topology', '-c', 'topology/Simple.topo', '-sibra') )
    check_output(('./supervisor/supervisor.sh', 'reload'))

def start_scion():
    check_output(('./scion.sh', 'start', 'nobuild'))
    time.sleep(0.1)

def stop_scion():
    check_output(('./scion.sh', 'stop'))

class Server:
    def __init__(self, ia):
        'ia e.g. 1-ff00:0:110'
        self.ia = ia
        self.process = None
    
    def run(self):
        cmd = ('./bin/sibra_bandwidth', 
               '-mode', 'server', 
               '-sciondFromIA', 
               '-local', '{},[127.0.0.1]:4444'.format(self.ia), 
               '-log.console', 'debug', 
               '-packetSize', '2000')
        self.process = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        time.sleep(0.2)

    def stop(self):
        if self.process.poll() == None:
            self.process.kill()
        self.process.wait()

    def __enter__(self):
        self.run()
        return self
    
    def __exit__(self, ex_type, ex_value, traceback):
        self.stop()

def run_client(server_ia, client_ia, duration=5, bwClass=14, bw=1000*1000, packetSize=2000):
    'Returns the speed'
    cmd = ('./bin/sibra_bandwidth', '-sciondFromIA', 
           '-remote', '{},[127.0.0.1]:4444'.format(server_ia), 
           '-local', '{},[127.0.0.1]:0'.format(client_ia), 
           '-packetSize', str(packetSize),
           '-log.console', 'debug',
           '-sibra=T', '-duration', str(duration),
           '-bw', str(bwClass),
           '-bandwidth', str(bw))
    out = check_output(cmd)
    lines = out.split('\n')
    for i in range(len(lines)-1, max(len(lines)-5, 0), -1):
        groups = CLIENT_REGEX.findall(lines[i])
        if groups:
            speed = float(groups[0][0])
            if groups[0][1] == 'Kbps':
                speed *= 1000
            elif groups[0][1] == 'Mbps':
                speed *= 1000 * 1000
            elif groups[0][1] == 'Gbps':
                speed *= 1000 * 1000 * 1000
            elif groups[0][1] == 'Tbps':
                speed *= 1000 * 1000 * 1000 * 1000
            drops = float(groups[0][2]) / 100
            return (speed, drops)
    raise Exception('Could not parse sibra_bandwidth client\'s output:\n{}'.format(out))


def check_speed(min, speed, max):
    ret = True
    if speed < min:
        print('Invalid speed {} < {}'.format(speed, min))
        ret = False
    if speed > max:
        print('Invalid speed {} > {}'.format(speed, max))
        ret = False
    return ret

def check_drops(min, drops, max):
    ret = True
    if drops < min:
        print('Invalid drops {} < {}'.format(drops, min))
        ret = False
    if drops > max:
        print('Invalid drops {} > {}'.format(drops, max))
        ret = False
    return ret

class Case1:
    'Case 1 modifies ff00:0:111 only'
    def __init__(self):
        self.sibra_path = os.path.join(SC, 'gen', 'ISD1', 'ASff00_0_111', 'sb1-ff00_0_111-1', 'sibra')
        with open(os.path.join(self.sibra_path, 'reservations.json')) as f:
            self.reservations = json.load(f)
        with open(os.path.join(self.sibra_path, 'matrix.yml')) as f:
            self.matrix = yaml.load(f)
    
    def forward(self):
        d = copy.deepcopy(self.reservations)
        d['Down-1-ff00:0:110']['DesiredSize'] = 14
        d['Down-1-ff00:0:110']['MaxSize'] = 20
        d['Down-1-ff00:0:110']['MinSize'] = 14
        d['Down-1-ff00:0:110']['SplitCls'] = 200
        d['Up-1-ff00:0:110']['DesiredSize'] = 14
        d['Up-1-ff00:0:110']['MaxSize'] = 20
        d['Up-1-ff00:0:110']['MinSize'] = 14
        d['Up-1-ff00:0:110']['SplitCls'] = 200
        with open(os.path.join(self.sibra_path, 'reservations.json'), 'w') as f:
            json.dump(d, f, indent=4, sort_keys=True)
        m = copy.deepcopy(self.matrix)
        for k in sorted(m.keys()):
            for j in sorted(m[k].keys()):
                m[k][j] = 9000000
        with open(os.path.join(self.sibra_path, 'matrix.yml'), 'w') as f:
            f.write(yaml.dump(m, default_flow_style=False))

    def backward(self):
        with open(os.path.join(self.sibra_path, 'reservations.json'), 'w') as f:
            json.dump(self.reservations, f, indent=4, sort_keys=True)
        with open(os.path.join(self.sibra_path, 'matrix.yml'), 'w') as f:
            f.write(yaml.dump(self.matrix, default_flow_style=False))

    def test(self):
        self.forward()
        start_scion()
        server_ia = '1-ff00:0:110'
        client_ia = '1-ff00:0:111'
        with Server(server_ia) as server:
            # TODO: remove the sleep and run constantly showpaths until we find one, or timeout
            # e.g. print(check_output( ('./bin/showpaths', '-sciondFromIA', '-srcIA', '1-ff00:0:111', '-dstIA', '1-ff00:0:110') ))
            time.sleep(8)
            speed, drops = run_client(server_ia, client_ia, bwClass=14, bw=99000000, duration=10)
            print('speed: {} \t drops: {}'.format(speed, drops))
            # target is 1.448 Mbps
            res = check_speed(1430000, speed, get_bw(14, 200)) # this fails SOMETIMES !
            # res = check_speed(1410000, speed, 1448000)
            res = check_drops(0.90, drops, 0.99) and res
        self.backward()
        return True if res else False



def main():
    stop_scion()
    gen_vanilla()
    c = Case1()
    print('Running Case1 ...')
    res = c.test()
    print('Case1 ', end='', flush=True)
    if res:
        print('OK')
    else:
        print('FAIL')


if __name__ == '__main__':
    sys.exit(main())


