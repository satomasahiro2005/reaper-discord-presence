-- Append the line below to the END of your existing
--   <REAPER resource path>/Scripts/__startup.lua
-- (create the file if it does not exist). Do NOT overwrite an __startup.lua
-- that already runs other startup scripts -- just add this one line.

dofile(reaper.GetResourcePath() .. "/Scripts/reaper_discord_presence.lua")
