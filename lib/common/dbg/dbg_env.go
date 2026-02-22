package dbg

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/log/v3"
)

// EnvString reads a string environment variable, printing it to stdout if set.
func EnvString(envVarName string, defaultVal string) string {
	v, _ := os.LookupEnv(envVarName)
	if v != "" {
		fmt.Printf("[dbg] env %s=%s\n", envVarName, v)
		return v
	}
	return defaultVal
}

// EnvBool reads a boolean environment variable ("true"/"false"), printing it to stdout if set.
func EnvBool(envVarName string, defaultVal bool) bool {
	v, _ := os.LookupEnv(envVarName)
	switch v {
	case "true":
		fmt.Printf("[dbg] env %s=%t\n", envVarName, true)
		return true
	case "false":
		fmt.Printf("[dbg] env %s=%t\n", envVarName, false)
		return false
	default:
		return defaultVal
	}
}

// EnvInt reads an integer environment variable (must be in range [0,4]), printing it to stdout if set.
func EnvInt(envVarName string, defaultVal int) int {
	v, _ := os.LookupEnv(envVarName)
	if v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			panic(err)
		}
		if i < 0 || i > 4 {
			panic(i)
		}
		fmt.Printf("[dbg] env %s=%d\n", envVarName, i)
		return i
	}
	return defaultVal
}

// EnvDataSize reads a datasize environment variable, printing it to stdout if set.
func EnvDataSize(envVarName string, defaultVal datasize.ByteSize) datasize.ByteSize {
	v, _ := os.LookupEnv(envVarName)
	if v != "" {
		val, err := datasize.ParseString(v)
		if err != nil {
			panic(err)
		}
		fmt.Printf("[dbg] env %s=%s\n", envVarName, val)
		return val
	}
	return defaultVal
}

// lazyEnvBool returns a function that lazily reads and caches a boolean environment variable.
// The env var is read at most once (on first call) and the result is cached via sync.OnceValue.
func lazyEnvBool(envVarName string) func() bool {
	return sync.OnceValue(func() bool {
		v, _ := os.LookupEnv(envVarName)
		if v == "true" {
			log.Info("[Experiment]", envVarName, true)
			return true
		}
		return false
	})
}

// lazyEnvString returns a function that lazily reads and caches a string environment variable.
func lazyEnvString(envVarName string) func() string {
	return sync.OnceValue(func() string {
		v, _ := os.LookupEnv(envVarName)
		if v != "" {
			log.Info("[Experiment]", envVarName, v)
		}
		return v
	})
}

// lazyEnvUint returns a function that lazily reads and caches an unsigned integer environment variable.
func lazyEnvUint(envVarName string) func() uint {
	return sync.OnceValue(func() uint {
		v, _ := os.LookupEnv(envVarName)
		if v != "" {
			i, err := strconv.Atoi(v)
			if err != nil {
				panic(err)
			}
			log.Info("[Experiment]", envVarName, uint(i))
			return uint(i)
		}
		return 0
	})
}

// lazyEnvUint64 returns a function that lazily reads and caches a uint64 environment variable.
func lazyEnvUint64(envVarName string) func() uint64 {
	return sync.OnceValue(func() uint64 {
		v, _ := os.LookupEnv(envVarName)
		if v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err == nil {
				log.Info("[Experiment]", envVarName, n)
				return n
			}
		}
		return 0
	})
}

// lazyEnvUint8 returns a function that lazily reads and caches a uint8 environment variable.
func lazyEnvUint8(envVarName string) func() uint8 {
	return sync.OnceValue(func() uint8 {
		v, _ := os.LookupEnv(envVarName)
		if i, _ := strconv.ParseUint(v, 10, 8); i > 0 {
			log.Info("[Experiment]", envVarName, uint8(i))
			return uint8(i)
		}
		return 0
	})
}

// lazyEnvDuration returns a function that lazily reads and caches a duration environment variable.
func lazyEnvDuration(envVarName string) func() time.Duration {
	return sync.OnceValue(func() time.Duration {
		v, _ := os.LookupEnv(envVarName)
		if v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				panic(err)
			}
			log.Info("[Experiment]", envVarName, d.String())
			return d
		}
		return 0
	})
}

// lazyEnvIntBounded returns a function that lazily reads and caches an integer env var
// that must be in range [0,4]. Panics on parse error or out-of-range.
func lazyEnvIntBounded(envVarName string) func() int {
	return sync.OnceValue(func() int {
		v, _ := os.LookupEnv(envVarName)
		if v != "" {
			i, err := strconv.Atoi(v)
			if err != nil {
				panic(err)
			}
			if i < 0 || i > 4 {
				panic(i)
			}
			log.Info("[Experiment]", envVarName, i)
			return i
		}
		return 0
	})
}
