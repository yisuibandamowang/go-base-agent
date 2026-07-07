-- queue_claim_atomic.lua
-- 当请求位于允许的队头窗口内时，进行出队 claim；同时清理过期僵尸条目
-- KEYS[1]: 队列 ZSET Key
-- ARGV[1]: 请求 ID
-- ARGV[2]: 最大可进入的 rank（可用许可数）
-- ARGV[3]: entry 存活标记 Key 前缀（Go 侧已 set with TTL，缺失即视为僵尸）
local queueKey = KEYS[1]
local requestId = ARGV[1]
local maxRank = tonumber(ARGV[2])
local entryPrefix = ARGV[3]

-- 取头部窗口 + 额外 slack：slack 用于在僵尸密集时尽量推进存活条目至 maxRank 之内
local slack = 16
local headEntries = redis.call('ZRANGE', queueKey, 0, maxRank + slack - 1)

local liveRank = -1
local liveCount = 0
for i = 1, #headEntries do
    local member = headEntries[i]
    if redis.call('EXISTS', entryPrefix .. member) == 1 then
        if member == requestId then
            liveRank = liveCount
        end
        liveCount = liveCount + 1
    else
        redis.call('ZREM', queueKey, member)
    end
end

if liveRank < 0 or liveRank >= maxRank then return {0} end

local score = redis.call('ZSCORE', queueKey, requestId)

redis.call('ZREM', queueKey, requestId)
redis.call('DEL', entryPrefix .. requestId)

return {1, score}
