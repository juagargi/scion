#!/bin/bash
# This script runs in a short-lived, host-networked Docker Compose service. It receives the TBF
# settings first, followed by the Linux bridge names selected from the generated topology.
set -euo pipefail

# Keep these as separate arguments: orchestration.py validates them before inserting them into the
# Compose command, and tc receives them without shell evaluation.
rate="$1"
burst="$2"
latency="$3"
shift 3

for bridge in "$@"; do
    # Docker connects each container endpoint to a bridge through a host-side veth.
	# Shaping every veth on an inter-AS bridge limits traffic in both directions across that simulated link.
    found=0
    while read -r veth; do
        [ -n "$veth" ] || continue
        found=1
        # replace is idempotent, so setup can be rerun after SCION has stopped or after tc values
        # have changed without first deleting an old qdisc.
        tc qdisc replace dev "$veth" root tbf \
            rate "$rate" burst "$burst" latency "$latency"
        # Fail setup if tc did not leave the expected token-bucket-filter qdisc in place.
        tc qdisc show dev "$veth" | awk '$1 == "qdisc" && $2 == "tbf" { found = 1 } END { exit !found }'
    # bridge prints names such as "veth123@if4"; tc needs only the host-side device name.
    done < <(bridge link show master "$bridge" | awk '{sub(/@.*/, "", $2); print $2}')
    if [ "$found" -eq 0 ]; then
        echo "no veth interfaces found on bridge $bridge" >&2
        exit 1
    fi
done
