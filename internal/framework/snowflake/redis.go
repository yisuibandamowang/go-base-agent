package snowflake

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const allocateNodeScript = `
local hash_key = KEYS[1]
local data_center_key = 'dataCenterId'
local worker_key = 'workId'
local max = 31
if redis.call('EXISTS', hash_key) == 0 then
    redis.call('HINCRBY', hash_key, data_center_key, 0)
    redis.call('HINCRBY', hash_key, worker_key, 0)
    return {0, 0}
end
local data_center = tonumber(redis.call('HGET', hash_key, data_center_key) or '0')
local worker = tonumber(redis.call('HGET', hash_key, worker_key) or '0')
if data_center == max and worker == max then
    redis.call('HSET', hash_key, data_center_key, 0)
    redis.call('HSET', hash_key, worker_key, 0)
    return {0, 0}
end
if worker ~= max then
    worker = redis.call('HINCRBY', hash_key, worker_key, 1)
    return {worker, data_center}
end
data_center = redis.call('HINCRBY', hash_key, data_center_key, 1)
redis.call('HSET', hash_key, worker_key, 0)
return {0, data_center}
`

const allocateNodeKey = "ragent:snowflake:worker"

// ConfigureFromRedis allocates a unique Snowflake node ID before the first ID is generated.
// Redis failure is returned to the caller so the service can choose its existing fallback policy.
func ConfigureFromRedis(ctx context.Context, client redis.UniversalClient) error {
	id, err := allocateNodeFromRedis(ctx, client)
	if err != nil {
		return err
	}
	return configureNode(id)
}

func allocateNodeFromRedis(ctx context.Context, client redis.UniversalClient) (int64, error) {
	if client == nil {
		return 0, fmt.Errorf("snowflake redis client is nil")
	}
	values, err := client.Eval(ctx, allocateNodeScript, []string{allocateNodeKey}).Result()
	if err != nil {
		return 0, fmt.Errorf("allocate snowflake node from redis: %w", err)
	}
	parts, ok := values.([]any)
	if !ok || len(parts) != 2 {
		return 0, fmt.Errorf("allocate snowflake node from redis: unexpected result %T", values)
	}
	worker, err := redisInt64(parts[0])
	if err != nil {
		return 0, fmt.Errorf("allocate snowflake worker id: %w", err)
	}
	datacenter, err := redisInt64(parts[1])
	if err != nil {
		return 0, fmt.Errorf("allocate snowflake data center id: %w", err)
	}
	if worker < 0 || worker > 31 || datacenter < 0 || datacenter > 31 {
		return 0, fmt.Errorf("snowflake node parts out of range: worker=%d datacenter=%d", worker, datacenter)
	}
	return worker + datacenter*32, nil
}

func redisInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected integer type %T", value)
	}
}
