local M = {}

local _cfg = {
    host     = nil,
    port     = 8883,
    username = nil,
    password = nil,
}

local function sq(s)
    return "'" .. tostring(s):gsub("'", "'\\''") .. "'"
end

local function base_opts()
    return "-h " .. sq(_cfg.host)
        .. " -p " .. sq(_cfg.port)
        .. " -u " .. sq(_cfg.username)
        .. " -P " .. sq(_cfg.password)
        .. " --tls-use-os-certs"
end

function M.configure(host, port, username, password)
    _cfg.host     = host
    _cfg.port     = port
    _cfg.username = username
    _cfg.password = password
end

function M.publish(topic, payload, retain, qos)
    local retain_flag = retain and "-r" or ""
    local qos_val     = qos or 0
    local cmd = "mosquitto_pub "
        .. base_opts()
        .. " -t " .. sq(topic)
        .. " -m " .. sq(payload)
        .. " -q " .. tostring(qos_val)
        .. " " .. retain_flag
    local rc = os.execute(cmd)
    return rc == 0
end

function M.subscribe_and_publish(response_topic, publish_topic, payload, timeout_secs)
    local tmpfile = "/tmp/waverms_resp_" .. tostring(os.time()) .. "_" .. tostring(math.random(10000, 99999))
    local timeout = timeout_secs or 30

    -- Subscribe first, then publish, then wait — runs synchronously so os.execute
    -- returns as soon as mosquitto_sub gets a message (or its -W timeout fires).
    local wrapper = "mosquitto_sub " .. base_opts()
        .. " -t " .. sq(response_topic)
        .. " -C 1 -W " .. tostring(timeout)
        .. " > " .. sq(tmpfile) .. " 2>/dev/null & SUB=$!; "
        .. "sleep 1; "
        .. "mosquitto_pub " .. base_opts()
        .. " -t " .. sq(publish_topic)
        .. " -m " .. sq(payload) .. " -q 1 2>/dev/null; "
        .. "wait $SUB"

    local utils = require "waverms.lib.utils"
    utils.log("DEBUG subscribe_and_publish cmd: " .. wrapper)
    os.execute(wrapper)

    local f = io.open(tmpfile, "r")
    local result = nil
    if f then
        local content = f:read("*a")
        f:close()
        os.remove(tmpfile)
        if content and content ~= "" then
            result = content:gsub("%s+$", "")
        end
    end

    return result
end

function M.subscribe_loop(device_id, lwt_topic, lwt_payload, on_connect, on_message, on_heartbeat, heartbeat_interval)
    local fifo = "/tmp/waverms_fifo_" .. device_id
    os.execute("rm -f " .. sq(fifo))
    os.execute("mkfifo " .. sq(fifo))

    local cmd_topic = "device/" .. device_id .. "/cmd"
    local interval  = heartbeat_interval or 60

    local wrapper = table.concat({
        "SUB_PID=''",
        "BEAT_PID=''",
        "mosquitto_sub " .. base_opts()
            .. " -t " .. sq(cmd_topic)
            .. " --will-topic " .. sq(lwt_topic)
            .. " --will-payload " .. sq(lwt_payload)
            .. " --will-retain"
            .. " --will-qos 1"
            .. " > " .. sq(fifo) .. " 2>/dev/null &",
        "SUB_PID=$!",
        "( while kill -0 $SUB_PID 2>/dev/null; do sleep " .. tostring(interval) .. "; printf '\\001\\n' > " .. sq(fifo) .. " 2>/dev/null; done ) &",
        "BEAT_PID=$!",
        "wait $SUB_PID",
        "kill $BEAT_PID 2>/dev/null",
        "wait $BEAT_PID 2>/dev/null",
    }, "; ")

    os.execute(wrapper .. " &")

    local fh = io.open(fifo, "r")
    if not fh then
        os.remove(fifo)
        return
    end

    on_connect()

    while true do
        local line = fh:read("*l")
        if line == nil then
            break
        elseif line == "\001" then
            on_heartbeat()
        else
            on_message(line)
        end
    end

    fh:close()
    os.remove(fifo)
end

return M
