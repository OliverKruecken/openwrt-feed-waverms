local M   = {}
local uci = require "uci"
local utils = require "waverms.lib.utils"

function M.apply(commands)
    local c      = uci.cursor()
    local output = {}
    local ok     = true

    for _, cmd in ipairs(commands) do
        local op, args = cmd:match("^uci%s+(%S+)%s*(.*)")
        if not op then
            table.insert(output, "unparseable command: " .. cmd)
            ok = false
            break
        end
        args = utils.trim(args)

        local success, err = pcall(function()
            if op == "set" then
                local pkg, section, option, value =
                    args:match("^(.-)%.(.-)%.(.-)=(.+)$")
                if not pkg then error("invalid set syntax: " .. args) end
                c:set(pkg, section, option, value)

            elseif op == "commit" then
                if args == "" then error("commit requires package name") end
                c:commit(args)

            elseif op == "delete" then
                local pkg, section, option = args:match("^(.-)%.(.-)%.(.+)$")
                if pkg then
                    c:delete(pkg, section, option)
                else
                    pkg, section = args:match("^(.-)%.(.+)$")
                    if not pkg then error("invalid delete syntax: " .. args) end
                    c:delete(pkg, section)
                end

            elseif op == "add" then
                local pkg, stype = args:match("^(%S+)%s+(%S+)$")
                if not pkg then error("invalid add syntax: " .. args) end
                c:add(pkg, stype)

            else
                error("unsupported UCI operation: " .. op)
            end
        end)

        if not success then
            table.insert(output, tostring(err))
            ok = false
            break
        end
        table.insert(output, cmd .. ": ok")
    end

    return ok, table.concat(output, "\n")
end

return M
