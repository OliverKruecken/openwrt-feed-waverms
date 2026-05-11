#!/bin/sh
# Command execution and ACK publishing.
# Expects MQTT_OPTS and DEVICE_ID to be set (exported) by the caller.

# shellcheck source=mqtt.sh
. /usr/share/waverms/lib/mqtt.sh

execute_command() {
    local raw_payload="$1"

    local cmd_id cmd_type timestamp
    cmd_id=$(echo "$raw_payload"   | jsonfilter -e '@.cmd_id')
    cmd_type=$(echo "$raw_payload" | jsonfilter -e '@.type')
    timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    if [ -z "$cmd_id" ]; then
        logger -t waverms "execute_command: missing cmd_id"
        return 1
    fi

    case "$cmd_type" in
        uci_set) _run_uci_commands "$raw_payload" "$cmd_id" "$timestamp" ;;
        *)
            logger -t waverms "Unknown command type: ${cmd_type}"
            _send_ack "$cmd_id" "error" "unknown command type: ${cmd_type}" "$timestamp"
            ;;
    esac
}

_run_uci_commands() {
    local payload="$1"
    local cmd_id="$2"
    local timestamp="$3"

    local status="ok"
    local combined_output=""
    local i=0

    while true; do
        local cmd
        cmd=$(echo "$payload" | jsonfilter -e "@.payload.commands[${i}]" 2>/dev/null)
        # jsonfilter exits non-zero or returns empty string when index is out of range
        if [ $? -ne 0 ] || [ -z "$cmd" ]; then
            break
        fi

        local result exit_code
        result=$(sh -c "$cmd" 2>&1)
        exit_code=$?

        if [ $exit_code -ne 0 ]; then
            status="error"
            combined_output="${combined_output}${cmd}: ${result}; "
            break
        fi
        combined_output="${combined_output}${cmd}: ok; "
        i=$((i + 1))
    done

    _send_ack "$cmd_id" "$status" "$combined_output" "$timestamp"
}

_send_ack() {
    local cmd_id="$1"
    local status="$2"
    local output="$3"
    local timestamp="$4"

    local output_escaped
    output_escaped=$(json_escape "$output")

    local ack_payload
    ack_payload="{\"cmd_id\":\"${cmd_id}\",\"status\":\"${status}\",\"output\":\"${output_escaped}\",\"timestamp\":\"${timestamp}\"}"

    mqtt_pub "device/${DEVICE_ID}/ack" "$ack_payload"
    logger -t waverms "ACK cmd_id=${cmd_id} status=${status}"
}