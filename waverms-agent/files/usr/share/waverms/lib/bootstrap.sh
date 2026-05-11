#!/bin/sh
# Bootstrap flow – runs once on first boot.
# Reads /etc/waverms/bootstrap_token, registers with the server,
# writes /etc/waverms/credentials, then removes the token.

CREDENTIALS="/etc/waverms/credentials"
BOOTSTRAP_TOKEN_FILE="/etc/waverms/bootstrap_token"

# shellcheck source=mqtt.sh
. /usr/share/waverms/lib/mqtt.sh

do_bootstrap() {
    load_config || return 1

    [ -f "$BOOTSTRAP_TOKEN_FILE" ] || {
        logger -t waverms "bootstrap_token not found – cannot register"
        return 1
    }

    TOKEN=$(cat "$BOOTSTRAP_TOKEN_FILE")
    [ -n "$TOKEN" ] || { logger -t waverms "bootstrap_token is empty"; return 1; }

    setup_bootstrap_mqtt "$TOKEN"

    # Gather hw_info
    MODEL=$(json_escape "$(cat /tmp/sysinfo/model 2>/dev/null || echo unknown)")
    OPENWRT_VER=$(json_escape "$(grep DISTRIB_RELEASE /etc/openwrt_release 2>/dev/null \
                  | cut -d= -f2 | tr -d '"')")
    AGENT_VER=$(json_escape "${AGENT_VERSION:-1.0.0}")

    local attempt=0
    local max_attempts=10
    local delay=1
    local resp_file

    while [ $attempt -lt $max_attempts ]; do
        attempt=$((attempt + 1))

        TMP_UUID=$(cat /proc/sys/kernel/random/uuid)
        resp_file="/tmp/waverms-bootstrap-$$-${attempt}"

        PAYLOAD="{\"tmp_id\":\"${TMP_UUID}\",\"bootstrap_token\":\"${TOKEN}\",\"hw_info\":{\"model\":\"${MODEL}\",\"openwrt_version\":\"${OPENWRT_VER}\",\"agent_version\":\"${AGENT_VER}\"}}"

        # Start subscriber first; -C 1: exit after one message; -W 30: timeout 30 s
        # shellcheck disable=SC2086
        mosquitto_sub ${MQTT_OPTS} -C 1 -W 30 \
            -t "bootstrap/${TMP_UUID}/response" \
            > "$resp_file" 2>/dev/null &
        local sub_pid=$!

        # Give the broker time to record the subscription before we publish
        sleep 1

        # shellcheck disable=SC2086
        mosquitto_pub ${MQTT_OPTS} \
            -t "bootstrap/register" \
            -m "$PAYLOAD" 2>/dev/null

        wait "$sub_pid"

        if [ -s "$resp_file" ]; then
            break
        fi

        rm -f "$resp_file"
        logger -t waverms "Bootstrap attempt ${attempt} timed out, retry in ${delay}s"
        sleep "$delay"
        delay=$((delay * 2))
        [ "$delay" -gt 300 ] && delay=300
    done

    if [ ! -s "$resp_file" ]; then
        logger -t waverms "Bootstrap failed after ${max_attempts} attempts"
        rm -f "$resp_file"
        return 1
    fi

    local response
    response=$(cat "$resp_file")
    rm -f "$resp_file"

    local device_id secret broker_host broker_port
    device_id=$(echo "$response"   | jsonfilter -e '@.device_id')
    secret=$(echo "$response"      | jsonfilter -e '@.secret')
    broker_host=$(echo "$response" | jsonfilter -e '@.broker_host' 2>/dev/null)
    broker_port=$(echo "$response" | jsonfilter -e '@.broker_port' 2>/dev/null)

    if [ -z "$device_id" ] || [ -z "$secret" ]; then
        logger -t waverms "Bootstrap: invalid response payload"
        return 1
    fi

    # Persist credentials (chmod 600 – no other user should read the secret).
    # Also store broker override if the server provided one.
    {
        printf 'DEVICE_ID=%s\nSECRET=%s\n' "$device_id" "$secret"
        [ -n "$broker_host" ] && printf 'BROKER_HOST=%s\n' "$broker_host"
        [ -n "$broker_port" ] && printf 'BROKER_PORT=%s\n' "$broker_port"
    } > "$CREDENTIALS"
    chmod 600 "$CREDENTIALS"

    # Remove the one-time bootstrap token
    rm -f "$BOOTSTRAP_TOKEN_FILE"

    logger -t waverms "Bootstrap complete: device_id=${device_id}"
    return 0
}