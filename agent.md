# WaveRMS Agent – Architecture & Implementation Guide

> Server architecture: `docs/architecture.md`
> CLAUDE.md: `CLAUDE.md`

---

## Overview

The WaveRMS Agent is an OpenWrt package that runs on managed devices. It is
responsible for:

- Registering with the WaveRMS server on first boot (bootstrap flow)
- Maintaining a persistent MQTT connection to the broker
- Sending periodic heartbeats with device state
- Receiving and executing UCI commands from the server
- Reporting command results back via ACK

---

## Language: Lua

The agent is implemented in **Lua** – the official scripting language of OpenWrt.

| | Shell | Lua | Go |
|---|---|---|---|
| Available on every OpenWrt | yes | yes | no (compiled binary) |
| Official OpenWrt language | no | **yes** | no |
| UCI bindings | via `uci` CLI | **native (`luci-lib-uci`)** | via CLI |
| JSON support | `jsonfilter` (fragile) | **`luci-lib-jsonc`** | stdlib |
| Error handling | limited | proper | proper |
| Unit testing | difficult | possible | easy |
| Binary size overhead | none | ~200KB interpreter | ~5–10MB |
| Reference implementation | – | **OpenWISP (openwisp-config)** | OpenSOHO |

Lua was chosen following the OpenWISP approach – the most battle-tested OpenWrt
management agent in production. The UCI and JSON libraries are native, the
interpreter is already present on every OpenWrt device, and the codebase is
significantly more maintainable than shell at scale.

---

## Feed Repository Structure

```
openwrt-feed-waverms/
└── waverms-agent/
    ├── Makefile
    └── files/
        ├── waverms-agent.init          → /etc/init.d/waverms-agent  (procd)
        └── usr/
            └── share/
                └── waverms/
                    ├── agent.lua       → main entry point
                    └── lib/
                        ├── bootstrap.lua   → first-boot registration
                        ├── mqtt.lua        → MQTT connection wrapper
                        ├── uci.lua         → UCI command execution
                        ├── config.lua      → read /etc/waverms/config (UCI)
                        └── utils.lua       → logging, JSON helpers
```

Agent code and batch-specific configuration are strictly separated:
- **Feed** contains all Lua code – versioned, reproducible
- **`files/` overlay** (ImageBuilder) contains only batch-specific config

---

## Package Makefile

```makefile
include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

include $(INCLUDE_DIR)/package.mk

define Package/waverms-agent
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=WaveRMS Remote Management Agent
  DEPENDS:=+lua +luasocket +luci-lib-uci +luci-lib-jsonc +mosquitto-client
endef

define Package/waverms-agent/description
  WaveRMS remote management agent for OpenWrt devices.
  Connects to the WaveRMS server via MQTT and executes
  configuration commands via UCI.
endef

define Package/waverms-agent/conffiles
/etc/waverms/config
endef

define Package/waverms-agent/install
  $(INSTALL_DIR) $(1)/usr/share/waverms/lib
  $(INSTALL_BIN)  ./files/usr/share/waverms/agent.lua        $(1)/usr/share/waverms/
  $(INSTALL_DATA) ./files/usr/share/waverms/lib/*.lua        $(1)/usr/share/waverms/lib/
  $(INSTALL_DIR)  $(1)/etc/init.d
  $(INSTALL_BIN)  ./files/waverms-agent.init                  $(1)/etc/init.d/waverms-agent
endef

$(eval $(call BuildPackage,waverms-agent))
```

**Notes:**
- `+luasocket` provides TCP/TLS for the MQTT connection
- `+luci-lib-uci` provides native UCI bindings (no subprocess needed)
- `+luci-lib-jsonc` provides fast C-backed JSON parsing
- `+mosquitto-client` provides `mosquitto_pub` / `mosquitto_sub` as fallback
- `conffiles` marks `/etc/waverms/config` as preserved on `opkg upgrade`

---

## Filesystem on Device

```
/etc/init.d/waverms-agent         ← procd service (from feed, chmod +x)
/etc/waverms/
├── config                        ← UCI config (from files/ overlay)
│                                    option broker_host 'waverms-broker.local'
│                                    option broker_port '8883'
│                                    option agent_version '1.0.0'
├── bootstrap_token               ← one-time token (from files/ overlay,
│                                    deleted after successful bootstrap)
└── credentials                   ← device_id + secret (chmod 600,
                                     created at runtime by bootstrap.lua)
/usr/share/waverms/
├── agent.lua
└── lib/
    ├── bootstrap.lua
    ├── mqtt.lua
    ├── uci.lua
    ├── config.lua
    └── utils.lua
```

---

## Configuration (UCI)

Config is stored in UCI format at `/etc/waverms/config`:

```
config waverms 'waverms'
    option broker_host  'waverms-broker.local'
    option broker_port  '8883'
    option agent_version '1.0.0'
    option heartbeat_interval '60'
    option reconnect_max_delay '300'
```

Read in Lua via `luci-lib-uci`:

```lua
local uci = require "uci"
local c   = uci.cursor()

local broker_host = c:get("waverms", "waverms", "broker_host")
local broker_port = tonumber(c:get("waverms", "waverms", "broker_port"))
```

---

## procd Init Script

```sh
#!/bin/sh /etc/rc.common

USE_PROCD=1
START=99
STOP=10

start_service() {
    procd_open_instance
    procd_set_param command /usr/bin/lua /usr/share/waverms/agent.lua
    procd_set_param respawn 3600 5 5   # threshold, timeout, retry
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
```

`START=99` ensures the agent starts after all network services.
`respawn` restarts the agent automatically if it crashes.

---

## Agent Flow

```
START (procd, after network-up)
│
├─► /etc/waverms/credentials exists?
│
├─ NO → bootstrap.lua:
│   1. Read bootstrap_token from /etc/waverms/bootstrap_token
│   2. Generate tmp_uuid (read /proc/sys/kernel/random/uuid)
│   3. MQTT connect: user="bootstrap", pw=bootstrap_token, TLS
│   4. SUBSCRIBE bootstrap/{tmp_uuid}/response
│   5. PUBLISH bootstrap/register {tmp_uuid, token, hw_info}
│   6. Wait for response (timeout 30s → retry with exponential backoff)
│   7. Parse {device_id, secret, broker_host} from JSON response
│   8. Write credentials to /etc/waverms/credentials (os.chmod 600)
│   9. Update broker_host in UCI config
│  10. Delete /etc/waverms/bootstrap_token (os.remove)
│  11. DISCONNECT → fall through to normal operation
│
└─ YES → agent.lua (normal operation):
    1. Read device_id + secret from /etc/waverms/credentials
    2. Resolve broker via config.lua (see Broker Discovery below)
    3. MQTT connect: user=device_id, pw=secret, TLS
       – LWT: device/{id}/status = "offline" (retain=true, qos=1)
    4. SUBSCRIBE device/{id}/cmd (qos=1)
    5. PUBLISH device/{id}/info (registration heartbeat)
    6. Event loop:
       a. Timer every 60s     → heartbeat on device/{id}/info
       b. Message on cmd topic → uci.lua executes commands
                               → PUBLISH device/{id}/ack
       c. Connection lost     → exponential backoff reconnect
                                 (1s → 2s → 4s → ... → 300s max)
```

---

## Broker Discovery (priority order)

```lua
-- lib/config.lua
local M = {}
local uci = require "uci"

function M.get_broker()
    local c = uci.cursor()

    -- 1. UCI config (set after first bootstrap, most reliable)
    local host = c:get("waverms", "waverms", "broker_host")
    if host and host ~= "" then return host end

    -- 2. DNS lookup
    local socket = require "socket"
    local ip = socket.dns.toip("waverms-broker.local")
    if ip then return ip end

    -- 3. Fallback: read raw config file
    local f = io.open("/etc/waverms/config", "r")
    if f then
        for line in f:lines() do
            local h = line:match("option broker_host%s+'(.-)'")
            if h then f:close(); return h end
        end
        f:close()
    end

    return nil
end

return M
```

---

## MQTT Topic Schema

| Topic | Direction | QoS | Retain | When |
|---|---|---|---|---|
| `bootstrap/register` | Agent → Server | 1 | false | First boot |
| `bootstrap/{tmp-uuid}/response` | Server → Agent | 1 | false | Response to register |
| `device/{id}/info` | Agent → Server | 1 | false | On connect + every 60s |
| `device/{id}/cmd` | Server → Agent | 1 | false | Command push |
| `device/{id}/ack` | Agent → Server | 1 | false | After execution |
| `device/{id}/status` | Broker LWT | 1 | **true** | On connection loss |

`device-id` is always a server-generated UUID – never a MAC address.

---

## Payload Formats

**`bootstrap/register`**
```json
{
  "tmp_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "bootstrap_token": "3f7a9c2e-...",
  "hw_info": {
    "model": "GL-iNet GL-MT300N-V2",
    "openwrt_version": "23.05.0",
    "agent_version": "1.0.0"
  }
}
```

**`bootstrap/{tmp-uuid}/response`**
```json
{
  "device_id": "550e8400-e29b-41d4-a716-446655440000",
  "secret": "a3f8c2e1d4b7...",
  "broker_host": "waverms-broker.example.com",
  "broker_port": 8883
}
```

**`device/{id}/info`** (on connect + every 60s)
```json
{
  "device_id": "550e8400-e29b-41d4-a716-446655440000",
  "hostname": "router-01",
  "uptime_seconds": 3600,
  "agent_version": "1.0.0",
  "timestamp": "2025-05-09T10:00:00Z"
}
```

**`device/{id}/cmd`**
```json
{
  "cmd_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "type": "uci_set",
  "payload": {
    "commands": [
      "uci set system.@system[0].hostname=router-01",
      "uci commit system"
    ]
  }
}
```

**`device/{id}/ack`**
```json
{
  "cmd_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "status": "ok",
  "output": "",
  "timestamp": "2025-05-09T10:00:05Z"
}
```

---

## UCI Command Execution (uci.lua)

```lua
-- lib/uci.lua
local M   = {}
local uci = require "uci"
local log = require "waverms.lib.utils"

function M.apply(commands)
    local c      = uci.cursor()
    local output = {}
    local ok     = true

    for _, cmd in ipairs(commands) do
        -- parse "uci set pkg.section.option=value"
        local op, args = cmd:match("^uci%s+(%S+)%s+(.*)")
        if not op then
            table.insert(output, "unparseable command: " .. cmd)
            ok = false
            break
        end

        local success, err = pcall(function()
            if op == "set" then
                local pkg, section, option, value =
                    args:match("^(.-)%.(.-)%.(.-)=(.+)$")
                c:set(pkg, section, option, value)

            elseif op == "commit" then
                c:commit(args)

            elseif op == "delete" then
                local pkg, section, option = args:match("^(.-)[%.](.-)[%.](.*)")
                if option ~= "" then
                    c:delete(pkg, section, option)
                else
                    c:delete(pkg, section)
                end

            else
                error("unsupported UCI op: " .. op)
            end
        end)

        if not success then
            table.insert(output, tostring(err))
            ok = false
            break
        end
    end

    return ok, table.concat(output, "\n")
end

return M
```

---

## Security Model on Device

| File | Permissions | Contents | Lifecycle |
|---|---|---|---|
| `/etc/waverms/bootstrap_token` | 600 | one-time token (from files/ overlay) | deleted after bootstrap |
| `/etc/waverms/credentials` | 600 | `device_id\nsecret` | created by bootstrap.lua, never deleted |
| `/etc/waverms/config` | 644 | UCI config, broker host | preserved on opkg upgrade |

- Credentials are never logged
- Bootstrap token is deleted immediately after successful registration
- All MQTT connections use TLS – credentials never travel in plaintext

---

## Image Build Workflow

**One-time feed setup:**
```bash
# feeds.conf.default
echo "src-git waverms https://github.com/yourcompany/openwrt-feed-waverms.git" \
  >> feeds.conf.default

./scripts/feeds update waverms
./scripts/feeds install -a -p waverms
```

**Per-batch build:**
```bash
# 1. Create token via WaveRMS API
# POST /api/v1/admin/bootstrap-tokens {"label":"batch-2025-05","maxUsage":500}

# 2. Write token to overlay
echo "<token>" > imagebuilder/files/etc/waverms/bootstrap_token

# 3. Build
make -j$(nproc) CONFIG_PACKAGE_waverms-agent=y

# 4. Partial rebuild after agent code change only
make package/waverms-agent/compile -j$(nproc)
make package/index
```

---

## Reference: OpenWISP Agent

The OpenWISP `openwisp-config` agent is the primary reference implementation.
Key differences from WaveRMS:

| | OpenWISP | WaveRMS |
|---|---|---|
| Protocol | HTTP polling | MQTT push |
| Auth | shared_secret (HMAC) | device-individual MQTT credentials |
| Config format | NetJSON | UCI commands |
| Registration | shared_secret + UUID | bootstrap token → device secret |
| Source | https://github.com/openwisp/openwisp-config | `openwrt-feed-waverms` |

---

*Version: 1.0 | May 2025*
