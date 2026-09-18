-- A token bucket. KEYS[1] holds up to ARGV[1] tokens, refilled evenly over
-- ARGV[2] milliseconds, and every request takes one. Redis runs a script all
-- at once, so two gateways can never both take the last token.
local limit = tonumber(ARGV[1])
local period = tonumber(ARGV[2])

-- Redis's clock, not ours: every gateway instance agrees on the time.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

-- A new bucket starts full. Then add what trickled in since the last request.
local bucket = redis.call('HMGET', KEYS[1], 'tokens', 'at')
local tokens = tonumber(bucket[1]) or limit
local at = tonumber(bucket[2]) or now
tokens = math.min(limit, tokens + (now - at) * limit / period)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'at', now)
-- After a whole period without requests the bucket is full: forget it.
redis.call('PEXPIRE', KEYS[1], period)

-- Milliseconds until the next whole token.
local wait = math.ceil(math.max(0, 1 - tokens) * period / limit)
return {allowed, math.floor(tokens), wait}
