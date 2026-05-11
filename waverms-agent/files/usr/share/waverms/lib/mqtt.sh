#!/bin/sh
# Shared MQTT helpers – source this file, do not execute directly.

WAVERMS_CONFIG="/etc/waverms/config"

load_config() {
    [ -f "$WAVERMS_CONFIG" ] || {
        logger -t waverms "ERROR: config missing: $WAVERMS_CONFIG"
        return 1
    }
    # shellcheck source=/dev/null
    . "$WAVERMS_CONFIG"
}

# Escape a string for embedding inside a JSON double-quoted value.
json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' | tr -d '\n'
}

# Set the global MQTT_OPTS for a bootstrap connection.
# Expects: BROKER_HOST, BROKER_PORT, CA_CERT already loaded from config.
setup_bootstrap_mqtt() {
    local token="$1"
    MQTT_OPTS="-h ${BROKER_HOST} -p ${BROKER_PORT:-8883} \
               -u bootstrap -P ${token} \
               --cafile ${CA_CERT:-/etc/waverms/ca.crt}"
    export MQTT_OPTS
}

# Set the global MQTT_OPTS for a normal device connection.
# Expects: BROKER_HOST, BROKER_PORT, CA_CERT already loaded from config.
setup_device_mqtt() {
    local device_id="$1"
    local secret="$2"
    MQTT_OPTS="-h ${BROKER_HOST} -p ${BROKER_PORT:-8883} \
               -u ${device_id} -P ${secret} \
               --cafile ${CA_CERT:-/etc/waverms/ca.crt}"
    export MQTT_OPTS
}

# Publish a single message.  Pass extra mosquitto_pub flags as $4 (e.g. "-r" for retain).
# shellcheck disable=SC2086
mqtt_pub() {
    local topic="$1"
    local payload="$2"
    local extra="${3:-}"
    mosquitto_pub ${MQTT_OPTS} -t "$topic" -m "$payload" ${extra} 2>/dev/null
}