#!/bin/bash
# This script runs in a short-lived helper that shares one border router's network namespace.
# Setup arguments are TBF settings followed by inter-AS peer addresses. Verify receives only peers.
set -euo pipefail

action="$1"
shift

device_for_peer() {
    local peer="$1"
    local device
    device=$(ip route get "$peer" | awk '{for (i = 1; i <= NF; i++) if ($i == "dev") {print $(i + 1); exit}}')
    if [ -z "$device" ]; then
        echo "no egress device found for inter-AS peer $peer" >&2
        exit 1
    fi
    echo "$device"
}

require_tbf() {
    local device="$1"
    tc qdisc show dev "$device" | awk \
        '$1 == "qdisc" && $2 == "tbf" && $3 != "0:" { found = 1 } END { exit !found }'
}

stat_value() {
    local name="$1"
    awk -v name="$name" '
        {
            for (i = 1; i <= NF; i++) {
                key = $i
                gsub(/[^A-Za-z_]/, "", key)
                if (key == name) {
                    value = $(i + 1)
                    gsub(/[^0-9].*/, "", value)
                    print value
                    exit
                }
            }
        }
    '
}

setup() {
    local rate="$1"
    local burst="$2"
    local limit="$3"
    shift 3

    if [ "$#" -eq 0 ]; then
        echo "no inter-AS peers supplied" >&2
        exit 1
    fi
    local peer device dropped
    declare -A configured=()
    for peer in "$@"; do
        device=$(device_for_peer "$peer")
        [ -z "${configured[$device]:-}" ] || continue
        configured[$device]=1
        tc qdisc replace dev "$device" root tbf rate "$rate" burst "$burst" limit "$limit"
        require_tbf "$device"
        dropped=$(tc -s qdisc show dev "$device" | stat_value dropped)
        if [ -z "$dropped" ]; then
            echo "unable to parse initial TBF drop count for $device" >&2
            tc -s qdisc show dev "$device" >&2
            exit 1
        fi
        if [ "$dropped" -ne 0 ]; then
            echo "new TBF on $device already reports drops" >&2
            tc -s qdisc show dev "$device" >&2
            exit 1
        fi
    done
}

verify() {
    if [ "$#" -eq 0 ]; then
        echo "no inter-AS peers supplied" >&2
        exit 1
    fi
    local peer device stats dropped overlimits backlog_bytes attempt
    declare -A verified=()
    for peer in "$@"; do
        device=$(device_for_peer "$peer")
        [ -z "${verified[$device]:-}" ] || continue
        verified[$device]=1
        require_tbf "$device"

        # Client traffic has stopped. The socket/TBF pipeline is deliberately bounded, so five
        # seconds is ample drain time even though low-rate BFD packets continue to be generated.
        backlog_bytes=-1
        for ((attempt = 0; attempt < 50; attempt++)); do
            stats=$(tc -s qdisc show dev "$device")
            backlog_bytes=$(stat_value backlog <<<"$stats")
            [ "${backlog_bytes:-1}" -eq 0 ] && break
            sleep 0.1
        done
        stats=$(tc -s qdisc show dev "$device")
        dropped=$(stat_value dropped <<<"$stats")
        overlimits=$(stat_value overlimits <<<"$stats")
        backlog_bytes=$(stat_value backlog <<<"$stats")
        if [ -z "$dropped" ] || [ -z "$overlimits" ] || [ -z "$backlog_bytes" ]; then
            echo "unable to parse TBF statistics for $device" >&2
            echo "$stats" >&2
            exit 1
        fi
        if [ "$dropped" -ne 0 ]; then
            echo "TBF on $device dropped $dropped packets" >&2
            exit 1
        fi
        if [ "$backlog_bytes" -ne 0 ]; then
            echo "TBF backlog on $device did not drain: ${backlog_bytes}b" >&2
            exit 1
        fi
        echo "HUMMBWTESTER_TC_STATS dev=$device dropped=$dropped overlimits=$overlimits backlog_bytes=$backlog_bytes"
    done
}

stats() {
    if [ "$#" -eq 0 ]; then
        echo "no inter-AS peers supplied" >&2
        exit 1
    fi
    local peer device output dropped overlimits backlog_bytes
    declare -A reported=()
    for peer in "$@"; do
        device=$(device_for_peer "$peer")
        [ -z "${reported[$device]:-}" ] || continue
        reported[$device]=1
        require_tbf "$device"
        output=$(tc -s qdisc show dev "$device")
        dropped=$(stat_value dropped <<<"$output")
        overlimits=$(stat_value overlimits <<<"$output")
        backlog_bytes=$(stat_value backlog <<<"$output")
        if [ -z "$dropped" ] || [ -z "$overlimits" ] || [ -z "$backlog_bytes" ]; then
            echo "unable to parse TBF statistics for $device" >&2
            echo "$output" >&2
            exit 1
        fi
        echo "HUMMBWTESTER_TC_STATS peer=$peer dev=$device dropped=$dropped overlimits=$overlimits backlog_bytes=$backlog_bytes"
    done
}

case "$action" in
    setup)
        setup "$@"
        ;;
    verify)
        verify "$@"
        ;;
    stats)
        stats "$@"
        ;;
    *)
        echo "usage: $0 setup RATE BURST LIMIT PEER... | verify PEER... | stats PEER..." >&2
        exit 2
        ;;
esac
