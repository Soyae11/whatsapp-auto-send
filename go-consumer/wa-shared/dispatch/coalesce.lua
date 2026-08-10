local key   = ARGV[1]
local ttl   = tonumber(ARGV[2])
local maxn  = tonumber(ARGV[3])
local force = ARGV[4] == '1'

if not force then
  local open = redis.call('HGET', KEYS[1], 'key')
  if open then
    local count = tonumber(redis.call('HGET', KEYS[1], 'n') or '0')
    if count < maxn then
      redis.call('HINCRBY', KEYS[1], 'n', 1)
      return { open, tostring(count + 1) }
    end
  end
end

redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'key', key, 'n', 1)
redis.call('PEXPIRE', KEYS[1], ttl)
return { key, '1' }
