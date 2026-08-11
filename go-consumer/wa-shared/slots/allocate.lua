-- Atomic slot allocation for one session.
--
-- The whole point of doing this in Lua is that GET-then-SET from Go is a race: two
-- concurrent enqueues both read the same stored timestamp, both add the gap, and both
-- schedule for the same instant. That is the burst this service exists to prevent,
-- reintroduced silently — nothing errors, the messages just go out together.
--
-- KEYS[1] = wa:slot:{sessionID}         tail: the last allocated slot, epoch ms
-- KEYS[2] = wa:slot:{sessionID}:head    the last slot handed to an expedited job, epoch ms
-- ARGV[1] = now          epoch ms, supplied by the caller
-- ARGV[2] = gap          ms between ordinary slots
-- ARGV[3] = horizon      ms; refuse to allocate further out than this
-- ARGV[4] = ttl          ms to keep both keys alive
-- ARGV[5] = '1' to expedite (critical lane), anything else to append
-- ARGV[6] = criticalGap  ms minimum spacing used for expedited sends instead of `gap`
--
-- Returns { slot_ms, rejected, expedited } as three strings. Strings rather than integers
-- because a Lua number is a double and epoch-millisecond values deserve no rounding
-- surprises.

local now       = tonumber(ARGV[1])
local gap       = tonumber(ARGV[2])
local horizon   = tonumber(ARGV[3])
local ttl       = tonumber(ARGV[4])
local expedite  = ARGV[5] == '1'
local criticalGap = tonumber(ARGV[6])

local tail = tonumber(redis.call('GET', KEYS[1]) or '0')

-- next = max(now, tail) + gap
local base = now
if tail > base then
  base = tail
end
local append = base + gap

-- An expedited job is paced by `criticalGap` against the last expedited send (`head`) only —
-- deliberately NOT against the ordinary lattice (`tail`/`gap`) at all. Anchoring it to the
-- next ordinary slot, even loosely, means a saturated backlog drags a "near-term" critical
-- send out to nearly a full `gap` away, which defeats the entire point of a separate lane.
-- `head` still keeps a burst of critical messages fanned out by `criticalGap` instead of
-- stacking on one instant; `Concurrency: 1` is what actually serialises both lanes onto the
-- session's real send timeline, so this is a shorter minimum spacing, not no spacing at all.
if expedite then
  local head = tonumber(redis.call('GET', KEYS[2]) or '0')

  local slot = now
  if head + criticalGap > slot then
    slot = head + criticalGap
  end

  -- The horizon is deliberately not consulted here. It exists to reject bulk that has
  -- backed up beyond usefulness; refusing an OTP because marketing filled the queue is
  -- exactly the failure this phase removes. The expedited slot is near-term by
  -- construction, and the tail still moves out by a full ordinary `gap`, so the session's
  -- total capacity is unchanged and ordinary traffic is what gets rejected once it
  -- saturates — a flood of "critical" sends cannot be used to dodge that budget.
  redis.call('SET', KEYS[1], tostring(append), 'PX', ttl)
  redis.call('SET', KEYS[2], tostring(slot), 'PX', ttl)
  return { tostring(slot), '0', '1' }
end

-- A backlog deeper than the horizon means something upstream is wrong. Reject rather
-- than schedule a message for next Tuesday.
--
-- The rejection deliberately does NOT advance the stored slot: a refused enqueue must
-- not consume pacing capacity, or a caller retrying into a full queue would push the
-- horizon further out with every attempt and lock the session out for good.
if (append - now) > horizon then
  return { tostring(append), '1', '0' }
end

redis.call('SET', KEYS[1], tostring(append), 'PX', ttl)
return { tostring(append), '0', '0' }
