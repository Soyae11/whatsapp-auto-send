-- Opening an already-open circuit is not always a no-op: a human pausing a session outranks a
-- breaker that tripped on its own, and SETNX cannot express that. Read-then-write has to be one
-- step, or two racing callers each decide to overwrite and the loser's state wins.
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

-- A pause has nothing to take over from another pause: rewriting would only restamp opened_at
-- and report a change that did not happen.
local ok, decoded = pcall(cjson.decode, current)
if ok and type(decoded) == 'table' and decoded.reason == ARGV[3] then
  return 0
end

redis.call('SET', KEYS[1], ARGV[1])
return 1
