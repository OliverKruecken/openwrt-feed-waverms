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
- `+mosquitto-client` provides `mosquitto_pub` / `mosquitto_sub` used by `mqtt.lua`
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
└── credentials                   ← KEY=VALUE pairs (chmod 600,
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

### Credentials file format

`bootstrap.lua` writes `/etc/waverms/credentials` as newline-separated
`KEY=VALUE` pairs (chmod 600):

```
DEVICE_ID=550e8400-e29b-41d4-a716-446655440000
SECRET=a3f8c2e1d4b7...
BROKER_HOST=waverms-broker.example.com
BROKER_PORT=8883
```

`BROKER_HOST` and `BROKER_PORT` are only written when the server includes them
in the bootstrap response. `agent.lua` reads this file and overrides the
UCI-derived config values if they are present.

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

`config.lua` exposes two public functions:

- `M.load()` – reads all known keys from UCI, falling back to built-in
  defaults for any key that is absent. Returns a plain Lua table.
- `M.get_broker(cfg)` – returns the broker host from the already-loaded `cfg`
  table; falls back to a DNS lookup of `waverms-broker.local`, then to a raw
  parse of `/etc/waverms/config`.
- `M.set_broker(host)` – writes the broker host back to UCI and commits.

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
    procd_set_param respawn 3600 5 0
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
```

`START=99` ensures the agent starts after all network services.
`respawn 3600 5 0` means: restart immediately on crash (0 retries before giving
up within the 3600 s window; 5 s minimum time between restarts).

---

## Agent Flow

```
START (procd, after network-up)
│
├─► /etc/waverms/credentials exists?
│
├─ NO → bootstrap.lua:
│   1. Read bootstrap_token from /etc/waverms/bootstrap_token
│   2. Read tmp_uuid from /proc/sys/kernel/random/uuid
│   3. MQTT configure: user="bootstrap", pw=bootstrap_token, TLS
│   4. For each attempt (max 10, exponential backoff 1 s → 300 s):
│      a. SUBSCRIBE bootstrap/{tmp_uuid}/response  (via mosquitto_sub -C 1)
│      b. PUBLISH   bootstrap/register {tmp_uuid, token, hw_info}
│      c. Wait for response (timeout 30 s)
│   5. Parse {device_id, secret, broker_host, broker_port} from JSON response
│   6. Write KEY=VALUE credentials to /etc/waverms/credentials (chmod 600)
│   7. Update broker_host in UCI config via config.set_broker()
│   8. Delete /etc/waverms/bootstrap_token (os.remove)
│   → return true; agent.lua reloads config and continues to normal operation
│
└─ YES → agent.lua (normal operation):
    1. Read DEVICE_ID, SECRET (and optionally BROKER_HOST, BROKER_PORT)
       from /etc/waverms/credentials
    2. Resolve broker via config.get_broker(cfg)
    3. MQTT configure: user=DEVICE_ID, pw=SECRET, TLS
       – LWT: device/{id}/status = "offline" (retain=true, qos=1)
    4. mqtt.subscribe_loop() – see "Subscribe Loop" below
    5. On session end: exponential backoff reconnect (1 s → 2 s → … → 300 s max)
```

### Subscribe Loop (mqtt.lua)

`subscribe_loop` cannot use a native Lua MQTT library because `luasocket` has
no MQTT support. Instead it drives `mosquitto_sub` / `mosquitto_pub` CLI tools
via a FIFO:

```
mosquitto_sub (background)  ──writes──►  /tmp/waverms_fifo_<device_id>
                                                   │
heartbeat ticker (background shell loop) ──writes──► (sentinel byte \001)
                                                   │
agent.lua  ──fh:read("*l")─────────────────────────►
    │
    ├── line == "\001"  → on_heartbeat()   (PUBLISH device/{id}/info every 60 s)
    ├── line == nil     → mosquitto_sub exited → return (triggers reconnect)
    └── any other line  → on_message(line)  → handle_command() → PUBLISH ack
```

Sequence on entry to `subscribe_loop`:

1. Create a named FIFO at `/tmp/waverms_fifo_<device_id>`.
2. Launch a shell wrapper (via `sh -c … &`) that:
   a. Starts `mosquitto_sub` with LWT options, writing to the FIFO.
   b. Starts a ticker subshell that writes `\001\n` to the FIFO every
      `heartbeat_interval` seconds (default 60).
   c. `wait`s for `mosquitto_sub` to exit, then kills the ticker.
3. Open the FIFO for reading in Lua (`io.open`).
4. Call `on_connect()` (publishes `online` status + initial `device/{id}/info`).
5. Enter read loop until `fh:read("*l")` returns `nil`.
6. Close and remove the FIFO.

---

## Broker Discovery (priority order)

`config.get_broker(cfg)` resolves in this order:

1. `cfg.broker_host` – set from UCI or overridden from credentials file.
2. DNS lookup of `waverms-broker.local` via `socket.dns.toip()`.
3. Raw line-by-line parse of `/etc/waverms/config` looking for
   `option broker_host '...'`.

Returns `nil` if all three fail; the caller exits with `os.exit(1)`.

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

`broker_host` and `broker_port` are optional; the agent uses its UCI config
values if they are absent.

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
  "output": "uci set system.@system[0].hostname=router-01: ok\nuci commit system: ok",
  "timestamp": "2025-05-09T10:00:05Z"
}
```

`output` contains one `"<cmd>: ok"` line per successful command, or an error
message for the first failing command. `status` is `"ok"` or `"error"`.

---

## UCI Command Execution (uci.lua)

Supported operations: `set`, `commit`, `delete`, `add`.

```lua
-- lib/uci.lua (public API)
M.apply(commands)  -- commands: list of strings like "uci set pkg.sec.opt=val"
                   -- returns: ok (bool), output (string)
```

Each command string is parsed into `op` + `args`. A `pcall` wraps the UCI
cursor call so that one bad command aborts the batch and returns `"error"`.
Every successful command appends `"<cmd>: ok"` to the output list.

Supported command forms:

| Command | Example |
|---|---|
| `uci set pkg.section.option=value` | `uci set system.@system[0].hostname=r1` |
| `uci commit pkg` | `uci commit system` |
| `uci delete pkg.section.option` | `uci delete network.wan.dns` |
| `uci delete pkg.section` | `uci delete network.wan` |
| `uci add pkg sectiontype` | `uci add firewall rule` |

---

## utils.lua – Public API

| Function | Description |
|---|---|
| `utils.log(msg)` | Writes to syslog via `logger -t waverms` |
| `utils.read_file(path)` | Returns file contents as string, or `nil` |
| `utils.write_file(path, content, perms)` | Writes file; applies `chmod perms` if given |
| `utils.file_exists(path)` | Returns `true` if the file can be opened |
| `utils.timestamp()` | Returns UTC ISO-8601 string (`!%Y-%m-%dT%H:%M:%SZ`) |
| `utils.trim(s)` | Strips leading and trailing whitespace |
| `utils.random_id()` | Returns a numeric string based on `os.time()` + random suffix |

---

## Security Model on Device

| File | Permissions | Contents | Lifecycle |
|---|---|---|---|
| `/etc/waverms/bootstrap_token` | 600 | one-time token (from files/ overlay) | deleted after bootstrap |
| `/etc/waverms/credentials` | 600 | `KEY=VALUE` pairs: DEVICE_ID, SECRET, optionally BROKER_HOST, BROKER_PORT | created by bootstrap.lua, never deleted |
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

*Version: 1.1 | May 2026*
