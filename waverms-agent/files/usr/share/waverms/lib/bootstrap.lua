local M       = {}
local json    = require "luci.jsonc"
local utils   = require "waverms.lib.utils"
local mqtt    = require "waverms.lib.mqtt"
local cfg_mod = require "waverms.lib.config"

local CREDENTIALS     = "/etc/waverms/credentials"
local BOOTSTRAP_TOKEN = "/etc/waverms/bootstrap_token"
local MAX_ATTEMPTS    = 10
local RESP_TIMEOUT    = 30

function M.run(cfg)
    local token_raw = utils.read_file(BOOTSTRAP_TOKEN)
    if not token_raw then
        utils.log("bootstrap_token not found")
        return false
    end
    local token = utils.trim(token_raw)
    if token == "" then
        utils.log("bootstrap_token is empty")
        return false
    end

    local host = cfg_mod.get_broker(cfg)
    if not host then
        utils.log("no broker host configured")
        return false
    end
    mqtt.configure(host, cfg.broker_port, "bootstrap", token)

    local model = utils.trim(utils.read_file("/tmp/sysinfo/model") or "unknown")
    local openwrt_ver = "unknown"
    local rel = utils.read_file("/etc/openwrt_release")
    if rel then
        openwrt_ver = rel:match('DISTRIB_RELEASE="([^"]*)"') or "unknown"
    end

    local delay = 1
    for attempt = 1, MAX_ATTEMPTS do
        local tmp_uuid = utils.trim(utils.read_file("/proc/sys/kernel/random/uuid") or "")
        if tmp_uuid == "" then
            utils.log("failed to read UUID")
            return false
        end

        local payload = json.stringify({
            tmp_id          = tmp_uuid,
            bootstrap_token = token,
            hw_info = {
                model           = model,
                openwrt_version = openwrt_ver,
                agent_version   = cfg.agent_version,
            },
        })

        local resp_raw = mqtt.subscribe_and_publish(
            "bootstrap/" .. tmp_uuid .. "/response",
            "bootstrap/register",
            payload,
            RESP_TIMEOUT
        )

        if resp_raw then
            local resp = json.parse(resp_raw)
            if resp and resp.device_id and resp.secret then
                local lines = {
                    "DEVICE_ID=" .. resp.device_id,
                    "SECRET="    .. resp.secret,
                }
                if resp.broker_host then
                    lines[#lines+1] = "BROKER_HOST=" .. resp.broker_host
                end
                if resp.broker_port then
                    lines[#lines+1] = "BROKER_PORT=" .. tostring(resp.broker_port)
                end
                if not utils.write_file(CREDENTIALS, table.concat(lines, "\n") .. "\n", "600") then
                    utils.log("failed to write credentials")
                    return false
                end
                if resp.broker_host then cfg_mod.set_broker(resp.broker_host) end
                os.remove(BOOTSTRAP_TOKEN)
                utils.log("Bootstrap complete: device_id=" .. resp.device_id)
                return true
            end
            utils.log("Bootstrap attempt " .. attempt .. ": invalid response")
        else
            utils.log("Bootstrap attempt " .. attempt .. " timed out, retry in " .. delay .. "s")
        end

        os.execute("sleep " .. delay)
        delay = math.min(delay * 2, 300)
    end

    utils.log("Bootstrap failed after " .. MAX_ATTEMPTS .. " attempts")
    return false
end

return M
