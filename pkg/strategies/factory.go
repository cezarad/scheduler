package strategies

import (
	"github.com/spf13/viper"

	"github.com/ds-test-framework/scheduler/pkg/strategies/issue"
	"github.com/ds-test-framework/scheduler/pkg/strategies/nop"
	"github.com/ds-test-framework/scheduler/pkg/strategies/random"
	"github.com/ds-test-framework/scheduler/pkg/strategies/timeout"
	"github.com/ds-test-framework/scheduler/pkg/types"
)

const (
	ERR_INVALID_STRATEGY = "INVALID_STRATEGY"
)

func GetStrategyEngine(options *viper.Viper) (types.StrategyEngine, *types.Error) {
	switch options.GetString("type") {
	case "timeout":
		return timeout.NewTimeoutEngine(options), nil
	case "random":
		return random.NewRandomScheduler(), nil
	case "no-op":
		return nop.NewNopScheduler(), nil
	case "issue":
		return issue.NewIssueScheduler(), nil
	default:
		return nil, types.NewError(
			ERR_INVALID_STRATEGY,
			"Invalid strategy type provided",
		)
	}
}
