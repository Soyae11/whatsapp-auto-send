-- Atomic slot allocation for one session.
--
-- The whole point of doing this in Lua is that GET-then-SET from Go is a race: two
-- concurrent enqueues both read the same stored timestamp, both add the gap, and both
-- schedule for the same instant. That is the burst this service exists to prevent,
-- reintroduced silently — nothing errors, the messages just go out together.
--
-- KEYS[1] = wa:slot:{sessionID}         tail: the last allocated slot, epoch ms
-- KEYS[2] = wa:slot:{sessionID}:head    the last slot handed to an expedited job, epoch ms
-- ARGV[1] = now       epoch ms, supplied by the caller
-- ARGV[2] = gap       ms between slots
-- ARGV[3] = horizon   ms; refuse to allocate further out than this
-- ARGV[4] = ttl       ms to keep both keys alive
-- ARGV[5] = '1' to expedite (critical lane), anything else to append
--
-- Returns { slot_ms, rejected, expedited } as three strings. Strings rather than integers
-- because a Lua number is a double and epoch-millisecond values deserve no rounding
-- surprises.

local now      = tonumber(ARGV[1])
local gap      = tonumber(ARGV[2])
local horizon  = tonumber(ARGV[3])
local ttl      = tonumber(ARGV[4])
local expedite = ARGV[5] == '1'

local tail = tonumber(redis.call('GET', KEYS[1]) or '0')

-- next = max(now, tail) + gap
local base = now
if tail > base then
  base = tail
end
local append = base + gap

-- Every appended slot sits on a lattice anchored at the tail with spacing exactly `gap`, so
-- the earliest send still ahead of us is `tail - floor((tail-now)/gap) * gap`. An expedited
-- job takes the midpoint of the interval that follows it. That is the best any insertion
-- into a saturated schedule can do: it splits one interval into two halves of `gap/2` and
-- leaves every other interval untouched, so no two sends ever land closer than half a gap.
-- Placing it at the front of the interval instead would collide head-on with a job already
-- enqueued at that instant, and `Concurrency: 1` would then emit both back to back.
--
-- `head` spaces expedited jobs from each other, so a flood of critical messages interleaves
-- into successive midpoints rather than stacking on one.
if expedite and tail > now then
  local ahead = math.floor((tail - now) / gap)
  local front = tail - ahead * gap
  local slot  = front + math.floor(gap / 2)

  local head = tonumber(redis.call('GET', KEYS[2]) or '0')
  if head + gap > slot then
    slot = head + gap
  end

  if slot < append then
    -- The horizon is deliberately not consulted here. It exists to reject bulk that has
    -- backed up beyond usefulness; refusing an OTP because marketing filled the queue is
    -- exactly the failure this phase removes. The expedited slot is near-term by
    -- construction, and the tail still moves out, so the session's total capacity is
    -- unchanged and ordinary traffic is what gets rejected once it saturates.
    redis.call('SET', KEYS[1], tostring(append), 'PX', ttl)
    redis.call('SET', KEYS[2], tostring(slot), 'PX', ttl)
    return { tostring(slot), '0', '1' }
  end
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
