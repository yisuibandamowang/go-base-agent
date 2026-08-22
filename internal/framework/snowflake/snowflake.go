package snowflake

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	sf "github.com/bwmarrin/snowflake"
)

var (
	node             *sf.Node
	once             sync.Once
	configured       bool
	configuredNodeID int64
)

func configureNode(id int64) error {
	if id < 0 || id > 1023 {
		return fmt.Errorf("snowflake node id out of range: %d", id)
	}
	if configured || node != nil {
		return fmt.Errorf("snowflake node already initialized")
	}
	configuredNodeID = id
	configured = true
	return nil
}

func Node() *sf.Node {
	once.Do(func() {
		id := int64(1)
		if configured {
			id = configuredNodeID
		}
		if env := os.Getenv("SNOWFLAKE_WORKER_ID"); env != "" {
			n, err := strconv.ParseInt(env, 10, 64)
			if err != nil {
				panic(fmt.Errorf("snowflake: invalid worker ID: %w", err))
			}
			id = n
		}
		var err error
		node, err = sf.NewNode(id)
		if err != nil {
			panic(fmt.Errorf("snowflake: failed to create node: %w", err))
		}
	})
	return node
}

func NextID() int64 {
	return Node().Generate().Int64()
}

func NextIDStr() string {
	return Node().Generate().String()
}
