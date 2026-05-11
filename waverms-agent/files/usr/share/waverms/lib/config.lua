local M = {}
local uci = require "uci"

local DEFAULTS = {
    broker_host         = "waverms-broker.local",
    broker_port         = 8883,
    ca_cert             = "/etc/waverms/ca.crt",
    heartbeat_interval  = 60,
    reconnect_max_delay = 300,
    agent_version       = "1.0.0",
}

function M.load()
    local c = uci.cursor()
    local cfg = {}
    for k, default in pairs(DEFAULTS) do
        local v = c:get("waverms", "waverms", k)
        if v ~= nil then
            cfg[k] = type(default) == "number" and tonumber(v) or tostring(v)
        else
            cfg[k] = default
        end
    end
    return cfg
end

function M.get_broker(cfg)
    if cfg.broker_host and cfg.broker_host ~= "" then
        return cfg.broker_host
    end
    local ok, socket = pcall(require, "socket")
    if ok then
        local ip = socket.dns.toip("waverms-broker.local")
        if ip then return ip end
    end
    local f = io.open("/etc/waverms/config", "r")
    if f then
        for line in f:lines() do
            local h = line:match("option broker_host%s+'(.-)'")
            if h then f:close() return h end
        end
        f:close()
    end
    return nil
end

function M.set_broker(host)
    local c = uci.cursor()
    c:set("waverms", "waverms", "broker_host", host)
    c:commit("waverms")
end

return M
