#!/bin/sh
# WaveRMS device agent – main entry point.
# Managed by procd via /etc/init.d/waverms-agent.

CREDENTIALS="/etc/waverms/credentials"

# shellcheck source=lib/mqtt.sh
. /usr/share/waverms/lib/mqtt.sh
# shellcheck source=lib/uci_apply.sh
. /usr/share/waverms/lib/uci_apply.sh

# Publish a device/info heartbeat.
# Requires MQTT_OPTS and DEVICE_ID to be exported.
publish_info() {
    local hostname uptime_sec timestamp
    hostname=$(cat /proc/sys/kernel/hostname 2>/dev/null || echo unknown)
    uptime_sec=$(cut -d. -f1 /proc/uptime 2>/dev/null || echo 0)
    timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    local payload
    payload="{\"device_id\":\"${DEVICE_ID}\",\"hostname\":\"$(json_escape "$hostname")\",\"uptime_seconds\":${uptime_sec},\"agent_version\":\"$(json_escape "${AGENT_VERSION:-1.0.0}")\",\"timestamp\":\"${timestamp}\"}"

    mqtt_pub "device/${DEVICE_ID}/info" "$payload"
}

# One connected session.  Returns when the broker connection is lost.
run_session() {
    setup_device_mqtt "$DEVICE_ID" "$SECRET"

    # LWT options: broker publishes "offline" (retained) on unexpected disconnect
    local lwt_opts
    lwt_opts="--will-topic device/${DEVICE_ID}/status \
              --will-payload offline \
              --will-retain \
              --will-qos 1"

    # Announce online before opening the command subscription
    mqtt_pub "device/${DEVICE_ID}/status" "online" "-r"
    publish_info

    # Named pipe so mosquitto_sub (background writer) and the read loop
    # (foreground reader) can exchange messages without a subshell variable
    # scoping problem.
    local fifo="/tmp/waverms-cmd-$$"
    mkfifo "$fifo"

    # Start subscriber in background; LWT is registered on this connection.
    # shellcheck disable=SC2086
    mosquitto_sub ${MQTT_OPTS} ${lwt_opts} \
        -t "device/${DEVICE_ID}/cmd" \
        > "$fifo" 2>/dev/null &
    local sub_pid=$!

    # Heartbeat: publish info every 60 s as long as the subscriber is alive.
    (
        while kill -0 "$sub_pid" 2>/dev/null; do
            sleep 60
            kill -0 "$sub_pid" 2>/dev/null && publish_info
        done
    ) &
    local hb_pid=$!

    # Process incoming commands (unblocks mosquitto_sub's open on the FIFO).
    while IFS= read -r payload; do
        [ -n "$payload" ] && execute_command "$payload"
    done < "$fifo"

    # mosquitto_sub exited → FIFO closed → read loop fell through.
    kill "$hb_pid" 2>/dev/null
    wait "$hb_pid" 2>/dev/null
    kill "$sub_pid" 2>/dev/null
    wait "$sub_pid" 2>/dev/null
    rm -f "$fifo"

    logger -t waverms "Session ended for device_id=${DEVICE_ID}"
}

main() {
    load_config || exit 1

    # Bootstrap if we have no credentials yet
    if [ ! -f "$CREDENTIALS" ]; then
        if [ ! -f "/etc/waverms/bootstrap_token" ]; then
            logger -t waverms "No credentials and no bootstrap_token – exiting"
            exit 1
        fi
        # shellcheck source=lib/bootstrap.sh
        . /usr/share/waverms/lib/bootstrap.sh
        do_bootstrap || { logger -t waverms "Bootstrap failed – will be retried by procd"; exit 1; }
    fi

    # Load device identity; credentials may override BROKER_HOST/BROKER_PORT
    # shellcheck source=/dev/null
    . "$CREDENTIALS"
    export DEVICE_ID SECRET BROKER_HOST BROKER_PORT CA_CERT AGENT_VERSION

    [ -n "$DEVICE_ID" ] && [ -n "$SECRET" ] || {
        logger -t waverms "Credentials file malformed"
        exit 1
    }

    logger -t waverms "Starting agent: device_id=${DEVICE_ID}"

    # Main reconnect loop with exponential back-off (1 s → 5 min)
    local delay=1
    while true; do
        run_session
        logger -t waverms "Connection lost – reconnecting in ${delay}s"
        sleep "$delay"
        delay=$((delay * 2))
        [ "$delay" -gt 300 ] && delay=300
    done
}

main