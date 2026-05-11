math.randomseed(os.time())

local M = {}

function M.log(msg)
    local safe = tostring(msg):gsub("'", "'\\''")
    os.execute("logger -t waverms '" .. safe .. "'")
end

function M.read_file(path)
    local f = io.open(path, "r")
    if not f then return nil end
    local c = f:read("*a")
    f:close()
    return c
end

function M.write_file(path, content, perms)
    local f = io.open(path, "w")
    if not f then return false end
    f:write(content)
    f:close()
    if perms then
        os.execute("chmod " .. perms .. " '" .. path:gsub("'", "'\\''") .. "'")
    end
    return true
end

function M.file_exists(path)
    local f = io.open(path, "r")
    if f then f:close() return true end
    return false
end

function M.timestamp()
    return os.date("!%Y-%m-%dT%H:%M:%SZ")
end

function M.trim(s)
    return (tostring(s):gsub("^%s+", ""):gsub("%s+$", ""))
end

function M.random_id()
    return string.format("%d%d", os.time(), math.random(10000, 99999))
end

return M
