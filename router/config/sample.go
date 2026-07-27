// Copyright 2023 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

const routerConfigSample = `
# The receive buffer size in bytes. 0 means use system default.
# (default 0)
receive_buffer_size = 0

# The send buffer size in bytes. 0 means use system default.
# (default 0)
send_buffer_size = 0

# The number of fast-path processors.
# (default GOMAXPROCS)
num_processors = 8

# The number of slow-path processors.
# (default 1)
num_slow_processors = 1

# The maximum number of packets read from a network socket at once.
# (default 256)
ingress_batch_size = 256

# The capacity of each fast- and slow-path processor ingress queue.
# 0 derives the capacity from the number of connections, processors, and ingress batch size.
# (default 0)
processor_queue_size = 0

# The maximum number of packets written to a network socket at once.
# (default 256)
egress_batch_size = 256

# The capacity of each egress priority queue.
# (default 256)
egress_queue_size = 256
`
