-- Opening a circuit that is already open is not always a no-op: a human pausing a session
-- outranks a breaker that tripped on its own. SETNX alone could not express that — it kept the
-- automatic state, so the watcher went on treating the session as machine-opened and closed it
-- again after a healthy streak, resuming a session somebody had deliberately held down.
--
-- Read-then-write has to be one step, or two callers racing could each decide to overwrite and
-- the loser's state would win. A script is that step.
--
-- KEYS[1] = circuit state, present only while open
-- ARGV[1] = state json to store
-- ARGV[2] = the reason carried by ARGV[1]
-- ARGV[3] = the reason that outranks whatever is already stored
--
-- Returns 1 when this call wrote the state, 0 when it left what was already there.

local current = redis.call('GET', KEYS[1])

if not current then
  redis.call('SET', KEYS[1], ARGV[1])
  return 1
end

-- Something is already open, so only the outranking reason may take it over.
if ARGV[2] ~= ARGV[3] then
  return 0
end

-- ...and it has nothing to take over from another pause for the same reason, which would only
-- restamp opened_at and report a change that did not happen.
local ok, decoded = pcall(cjson.decode, current)
if ok and type(decoded) == 'table' and decoded.reason == ARGV[3] then
  return 0
end

redis.call('SET', KEYS[1], ARGV[1])
return 1
