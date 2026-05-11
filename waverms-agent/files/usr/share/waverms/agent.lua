package.path = "/usr/share/?.lua;" .. package.path

local json      = require "luci.jsonc"
local utils     = require "waverms.lib.utils"
local cfg_mod   = require "waverms.lib.config"
local mqtt      = require "waverms.lib.mqtt"
local uci_exec  = require "waverms.lib.uci"
local bootstrap = require "waverms.lib.bootstrap"

local CREDENTIALS = "/etc/waverms/credentials"

local function read_credentials()
    local content = utils.read_file(CREDENTIALS)
    if not content then return nil end
    local t = {}
    for line in (content .. "\n"):gmatch("([^\n]+)\n") do
        local k, v = line:match("^(%w+)=(.*)$")
        if k then t[k] = v end
    end
    return t
end

local function publish_info(device_id, cfg)
    local hostname = utils.trim(utils.read_file("/proc/sys/kernel/hostname") or "unknown")
    local uptime_raw = utils.read_file("/proc/uptime") or "0"
    local uptime = math.floor(tonumber(uptime_raw:match("^(%S+)")) or 0)
    local payload = json.stringify({
        device_id      = device_id,
        hostname       = hostname,
        uptime_seconds = uptime,
        agent_version  = cfg.agent_version,
        timestamp      = utils.timestamp(),
    })
    mqtt.publish("device/" .. device_id .. "/info", payload, false, 1)
end

local function handle_command(device_id, payload_str)
    local payload = json.parse(payload_str)
    if not payload then
        utils.log("execute_command: invalid JSON")
        return
    end
    local cmd_id   = payload.cmd_id
    local cmd_type = payload.type
    if not cmd_id then
        utils.log("execute_command: missing cmd_id")
        return
    end

    local status, output
    if cmd_type == "uci_set" then
        local commands = payload.payload and payload.payload.commands
        if not commands then
            status, output = "error", "missing payload.commands"
        else
            local ok, out = uci_exec.apply(commands)
            status = ok and "ok" or "error"
            output = out
        end
    else
        utils.log("Unknown command type: " .. tostring(cmd_type))
        status = "error"
        output = "unknown command type: " .. tostring(cmd_type)
    end

    local ack = json.stringify({
        cmd_id    = cmd_id,
        status    = status,
        output    = output or "",
        timestamp = utils.timestamp(),
    })
    mqtt.publish("device/" .. device_id .. "/ack", ack, false, 1)
    utils.log("ACK cmd_id=" .. cmd_id .. " status=" .. status)
end

local function run_session(device_id, secret, cfg)
    local host = cfg_mod.get_broker(cfg)
    if not host then
        utils.log("no broker host – cannot connect")
        return
    end
    mqtt.configure(host, cfg.broker_port, device_id, secret, cfg.ca_cert)

    mqtt.subscribe_loop(
        device_id,
        "device/" .. device_id .. "/status",
        "offline",
        function()
            mqtt.publish("device/" .. device_id .. "/status", "online", true, 1)
            publish_info(device_id, cfg)
        end,
        function(line) handle_command(device_id, line) end,
        function() publish_info(device_id, cfg) end,
        cfg.heartbeat_interval
    )

    utils.log("Session ended for device_id=" .. device_id)
end

local function main()
    local cfg = cfg_mod.load()

    if not utils.file_exists(CREDENTIALS) then
        if not utils.file_exists("/etc/waverms/bootstrap_token") then
            utils.log("No credentials and no bootstrap_token – exiting")
            os.exit(1)
        end
        if not bootstrap.run(cfg) then
            utils.log("Bootstrap failed – will be retried by procd")
            os.exit(1)
        end
        cfg = cfg_mod.load()
    end

    local creds = read_credentials()
    if not creds or not creds.DEVICE_ID or not creds.SECRET then
        utils.log("Credentials file malformed")
        os.exit(1)
    end

    if creds.BROKER_HOST then cfg.broker_host = creds.BROKER_HOST end
    if creds.BROKER_PORT then cfg.broker_port = tonumber(creds.BROKER_PORT) end

    utils.log("Starting agent: device_id=" .. creds.DEVICE_ID)

    local delay = 1
    while true do
        run_session(creds.DEVICE_ID, creds.SECRET, cfg)
        utils.log("Connection lost – reconnecting in " .. delay .. "s")
        os.execute("sleep " .. delay)
        delay = math.min(delay * 2, cfg.reconnect_max_delay)
    end
end

main()
