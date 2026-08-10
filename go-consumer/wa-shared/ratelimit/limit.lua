local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])

local count = redis.call('INCR', key)
if count == 1 then
  redis.call('PEXPIRE', key, window)
end

local ttl = redis.call('PTTL', key)
if ttl < 0 then
  ttl = window
  redis.call('PEXPIRE', key, window)
end

local allowed = 1
if count > limit then
  allowed = 0
  redis.call('DECR', key)
end

local remaining = limit - count
if remaining < 0 then
  remaining = 0
end

return {allowed, remaining, ttl}
