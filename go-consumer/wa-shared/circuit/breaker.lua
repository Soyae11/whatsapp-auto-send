-- Counting the failure and claiming the open transition must be one step. Two workers
-- failing at once would otherwise both read count == threshold-1, both cross it, and both
-- report a transition, producing duplicate pages for a single event.
--
-- KEYS[1] = consecutive failure counter
-- KEYS[2] = circuit state, present only while open
-- ARGV[1] = threshold
-- ARGV[2] = counter ttl in ms
-- ARGV[3] = state json to store on the transition
--
-- Returns { failures, opened } where opened is '1' for the caller that crossed the
-- threshold and '0' for everyone else.

local n = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])

if n < tonumber(ARGV[1]) then
  return { tostring(n), '0' }
end

if redis.call('SET', KEYS[2], ARGV[3], 'NX') then
  return { tostring(n), '1' }
end

return { tostring(n), '0' }
