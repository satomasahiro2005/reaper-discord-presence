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

-- NOTE: the project file name is intentionally NOT collected or written, so it
-- never leaves this machine and never appears in the Discord presence.

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

-- Raw name of the top FX (slot 0) on the first selected track, "" if none.
local function top_fx_name()
  local track = reaper.GetSelectedTrack(0, 0)
  if not track then return "" end
  if reaper.TrackFX_GetCount(track) < 1 then return "" end
  local _, name = reaper.TrackFX_GetFXName(track, 0, "")
  return name or ""
end

-- ---------------------------------------------------------------------------
-- main deferred loop
-- ---------------------------------------------------------------------------

local POLL_SECONDS = 2.0
local next_run = 0.0

-- Idle tracking: the fingerprint changes whenever the user plays, moves the
-- edit cursor, or edits the project. When it stops changing, idle time grows
-- and the daemon can hide the presence.
local last_fingerprint = nil
local last_activity = reaper.time_precise()

local function loop()
  local now = reaper.time_precise()
  if now >= next_run then
    next_run = now + POLL_SECONDS

    local version   = reaper.GetAppVersion()          -- e.g. "7.74/x64"
    local state     = reaper.GetPlayState()
    local transport = transport_name(state)
    local bpm       = reaper.Master_GetTempo()         -- project tempo, e.g. 120.0
    local fx        = top_fx_name()

    local fingerprint = string.format("%d|%.3f|%.3f|%d|%s",
      state,
      reaper.GetPlayPosition(),                -- moves continuously while playing
      reaper.GetCursorPosition(),              -- moves when the edit cursor moves
      reaper.GetProjectStateChangeCount(0),    -- increments on any edit
      fx)
    if fingerprint ~= last_fingerprint then
      last_fingerprint = fingerprint
      last_activity = now
    end
    local idle = now - last_activity

    local json = string.format(
      '{"app":"REAPER","version":"%s","transport":"%s","bpm":%.3f,"fx":"%s","idleSeconds":%.1f,"timestamp":%.3f}',
      json_escape(version),
      json_escape(transport),
      bpm,
      json_escape(fx),
      idle,
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
