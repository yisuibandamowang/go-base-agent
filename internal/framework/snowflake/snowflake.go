package snowflake

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	sf "github.com/bwmarrin/snowflake"
)

var (
	node *sf.Node
	once sync.Once
)

func Node() *sf.Node {
	once.Do(func() {
		id := int64(1)
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
