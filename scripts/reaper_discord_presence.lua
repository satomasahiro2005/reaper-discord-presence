--[[
  reaper_discord_presence.lua

  REAPER side of the "REAPER Discord Rich Presence (no Node)" setup.

  Responsibility (kept deliberately tiny):
    * Read REAPER state (version / project / transport).
    * Write it to a status JSON in the REAPER resource folder.
    * Launch the companion Go daemon (reaper-discord-presence.exe) once.

  The daemon watches the JSON and talks to Discord over local IPC.
  This script never touches Discord directly.

  Placed in:  %APPDATA%\REAPER\Scripts\reaper_discord_presence.lua
  Started by: __startup.lua  (dofile)
]]--

local res_path    = reaper.GetResourcePath()
local status_path = res_path .. "/reaper_discord_presence.json"
local exe_path    = res_path .. "/Scripts/reaper-discord-presence.exe"

-- ---------------------------------------------------------------------------
-- helpers
-- ---------------------------------------------------------------------------

local function json_escape(s)
  s = s or ""
  s = s:gsub("\\", "\\\\")
  s = s:gsub("\"", "\\\"")
  s = s:gsub("\n", "\\n")
  s = s:gsub("\r", "\\r")
  s = s:gsub("\t", "\\t")
  return s
end

-- REAPER GetPlayState() bit flags: 1=playing, 2=paused, 4=recording.
-- Check recording first (it also sets the playing bit), then paused, then playing.
local function transport_name(state)
  if (state & 4) == 4 then return "recording" end
  if (state & 2) == 2 then return "paused"    end
  if (state & 1) == 1 then return "playing"   end
  return "stopped"
end

-- Current project file name, e.g. "song.rpp" ("" when the project is unsaved).
-- reaper.GetProjectName returns the filename ONLY, as a single string; proj=0 is
-- the active project and the second argument is a required-but-ignored buffer.
local function project_name()
  return reaper.GetProjectName(0, "") or ""
end

local function launch_daemon()
  local f = io.open(exe_path, "rb")
  if not f then
    -- exe not deployed yet; nothing to launch.
    return
  end
  f:close()
  -- "start" detaches so REAPER is not blocked. The exe itself guards against
  -- a second instance via a lock file, so launching every REAPER start is safe.
  os.execute('start "" "' .. exe_path .. '"')
end

-- ---------------------------------------------------------------------------
-- main deferred loop
-- ---------------------------------------------------------------------------

local POLL_SECONDS = 2.0
local next_run = 0.0

local function loop()
  local now = reaper.time_precise()
  if now >= next_run then
    next_run = now + POLL_SECONDS

    local version   = reaper.GetAppVersion()          -- e.g. "7.74/x64"
    local project   = project_name()
    local state     = reaper.GetPlayState()
    local transport = transport_name(state)

    local json = string.format(
      '{"app":"REAPER","version":"%s","projectName":"%s","transport":"%s","timestamp":%.3f}',
      json_escape(version),
      json_escape(project),
      json_escape(transport),
      now
    )

    -- Write every poll. The file's mtime doubles as a liveness heartbeat: the
    -- daemon treats a file older than staleAfterMs as "REAPER closed" and clears
    -- the presence. So we must keep touching the file even when nothing changed,
    -- otherwise an idle (stopped, unchanged project) REAPER would look dead.
    -- The daemon de-duplicates content itself before calling Discord, so a tiny
    -- ~100-byte write every 2 s is harmless. (timestamp differs each write.)
    local f = io.open(status_path, "w")
    if f then
      f:write(json)
      f:close()
    end
  end

  reaper.defer(loop)
end

-- On REAPER quit (or this script being stopped) remove the status file so the
-- daemon sees it disappear and clears the Discord presence right away, instead
-- of waiting out the staleAfterMs timeout.
reaper.atexit(function()
  os.remove(status_path)
end)

launch_daemon()
loop()
