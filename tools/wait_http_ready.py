#!/usr/bin/env python3

# Copyright 2026 ETH Zurich
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

import argparse
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request


def ready(url: str, timeout: float) -> bool:
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            return 200 <= response.status < 300
    except (urllib.error.URLError, TimeoutError):
        return False


def main() -> int:
    parser = argparse.ArgumentParser(description="Wait for an HTTP endpoint to become ready.")
    parser.add_argument("--url", required=True, help="Endpoint to poll.")
    parser.add_argument("--timeout", type=float, default=60.0, help="Maximum wait in seconds.")
    parser.add_argument("--interval", type=float, default=1.0, help="Retry interval in seconds.")
    parser.add_argument("command", nargs=argparse.REMAINDER, help="Command to execute after readiness.")
    args = parser.parse_args()

    command = args.command
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        parser.error("missing command to execute")

    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        if ready(args.url, min(args.interval, 2.0)):
            os.execvp(command[0], command)
        time.sleep(args.interval)

    print(f"Timed out waiting for {args.url}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
