-- A sliding window over one user's transfers. KEYS[1] is a sorted set: each
-- member is a transfer, "id:amount", scored by when we first saw it. ARGV is
-- the member, then the count window and the amount window, in milliseconds.
-- Redis runs a script all at once: nobody else reads or writes in between.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local count_window = tonumber(ARGV[2])
local amount_window = tonumber(ARGV[3])

-- Forget what's older than the longer window. Then add this transfer. NX: a
-- retry of the same transfer keeps its first time, and counts once.
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - amount_window)
redis.call('ZADD', KEYS[1], 'NX', now, ARGV[1])
redis.call('PEXPIRE', KEYS[1], amount_window)

-- How many in the count window, and how much in the amount window.
local count = redis.call('ZCOUNT', KEYS[1], now - count_window, '+inf')
local amount = 0
for _, member in ipairs(redis.call('ZRANGE', KEYS[1], 0, -1)) do
  amount = amount + tonumber(string.match(member, ':(%d+)$'))
end
return {count, amount}
