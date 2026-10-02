package control

import (
	"encoding/json"
	"fmt"
	"time"

	"symphony/blocks/common/faults"
)

// The operations of /control, named once: the blocks register their
// handlers under these names and the engine sends them.
const (
	// every block
	OpConfigGet   = "config.get"
	OpConfigSet   = "config.set"
	OpConfigReset = "config.reset"
	OpFaultSet    = "fault.set"
	OpFaultClear  = "fault.clear"
	OpFaultList   = "fault.list"
	OpChaosCPU    = "chaos.cpu"
	OpChaosMemory = "chaos.memory"
	OpChaosStall  = "chaos.stall"
	OpChaosResume = "chaos.resume"
	OpChaosClear  = "chaos.clear"
	OpChaosCrash  = "chaos.crash"
	OpStateReset  = "state.reset"
	OpLogLevel    = "log.level"
	OpLinksGet    = "links.get"
	OpTargetsGet  = "targets.get"
	OpOps         = "ops"
	// gateway
	OpLoadSet      = "load.set"
	OpLoadStop     = "load.stop"
	OpLoadStatus   = "load.status"
	OpSagaDone     = "saga.done"
	OpSagasRecent  = "sagas.recent"
	OpSagasSummary = "sagas.summary"
	// service
	OpBehaviourGet   = "behaviour.get"
	OpBehaviourSet   = "behaviour.set"
	OpBehaviourReset = "behaviour.reset"
	OpWorkSet        = "work.set"
	OpBreakerSet     = "breaker.set"
	// service and database
	OpPoolSet = "pool.set"
	// broker
	OpTopicCreate    = "topic.create"
	OpTopicDelete    = "topic.delete"
	OpTopicsGet      = "topics.get"
	OpConsumerPause  = "consumer.pause"
	OpConsumerResume = "consumer.resume"
	OpDeliverySet    = "delivery.set"
	// database
	OpLatencySet = "latency.set"
	OpStoreSet   = "store.set"
	OpStoreFill  = "store.fill"
	OpStoreGet   = "store.get"
	OpStoreClear = "store.clear"
	// proxy
	OpPairsGet = "pairs.get"
	OpRouteSet = "route.set"
)

// readOps are the operations that only read a block's state.
var readOps = map[string]bool{
	OpConfigGet: true, OpFaultList: true, OpLinksGet: true, OpTargetsGet: true, OpOps: true,
	OpLoadStatus: true, OpSagasRecent: true, OpSagasSummary: true,
	OpBehaviourGet: true, OpTopicsGet: true, OpStoreGet: true, OpPairsGet: true,
}

// ReadOnly reports whether an operation only reads a block's state (the UI
// polls some of them, so the engine does not log them).
func ReadOnly(op string) bool { return readOps[op] }

// --- commands the engine sends ----------------------------------------------

// ConfigSet changes config keys of a running block.
func ConfigSet(patch map[string]any) Command { return Command{Op: OpConfigSet, Params: patch} }

// ConfigReset undoes every config.set since the block started.
func ConfigReset() Command { return Command{Op: OpConfigReset} }

// StateReset makes a block forget what it accumulated.
func StateReset() Command { return Command{Op: OpStateReset} }

// FaultSet injects a link fault. The scope keys among params (edge,
// direction, peer, topic, table) become the fault's target, the rest are
// the kind's knobs; d of 0 means until cleared.
func FaultSet(kind faults.Kind, params map[string]any, d time.Duration) Command {
	target, knobs := faults.SplitScope(params)
	knobs["kind"] = string(kind)
	return Command{Op: OpFaultSet, Target: target, Params: knobs, DurationMs: int(d.Milliseconds())}
}

// FaultClear removes one fault by the id fault.set returned.
func FaultClear(id string) Command {
	return Command{Op: OpFaultClear, Params: map[string]any{"id": id}}
}

// FaultClearAll removes every fault of a block.
func FaultClearAll() Command {
	return Command{Op: OpFaultClear, Params: map[string]any{"all": true}}
}

// Chaos starts a chaos operation (OpChaosCPU, OpChaosMemory, OpChaosStall,
// OpChaosCrash) with its knobs.
func Chaos(op string, params map[string]any, durationMs int) Command {
	return Command{Op: op, Params: params, DurationMs: durationMs}
}

// ChaosClear ends every burn, stall and held allocation.
func ChaosClear() Command { return Command{Op: OpChaosClear} }

// LoadSet starts the gateway's load generator.
func LoadSet(params map[string]any) Command { return Command{Op: OpLoadSet, Params: params} }

// LoadStop stops it.
func LoadStop() Command { return Command{Op: OpLoadStop} }

// LoadStatus asks what it is doing.
func LoadStatus() Command { return Command{Op: OpLoadStatus} }

// SagaDone reports a saga's outcome to the gateway (sinks send it).
func SagaDone(saga, outcome, node string) Command {
	return Command{Op: OpSagaDone, Params: map[string]any{"saga": saga, "outcome": outcome, "node": node}}
}

// TopicCreate makes sure a broker has a topic.
func TopicCreate(topic string) Command {
	return Command{Op: OpTopicCreate, Params: map[string]any{"topic": topic}}
}

// Behaviour sets a service's behaviour program, or with no program returns
// it to the one derived from its links.
func Behaviour(program json.RawMessage) (Command, error) {
	if len(program) == 0 {
		return Command{Op: OpBehaviourReset}, nil
	}
	var params map[string]any
	if err := json.Unmarshal(program, &params); err != nil {
		return Command{}, fmt.Errorf("behaviour program: %w", err)
	}
	return Command{Op: OpBehaviourSet, Params: params}, nil
}

// --- typed access to params and results ----------------------------------------

// Bind decodes the command's params into v (a struct with json tags).
func (c Command) Bind(v any) error {
	raw, err := json.Marshal(c.Params)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	return nil
}

// Decode reads the result's state into v. The state is whatever the
// operation's handler returned; on the engine's side of the wire it has
// been through JSON, which Decode undoes.
func (r Result) Decode(v any) error {
	raw, err := json.Marshal(r.State)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: unexpected state: %w", r.Op, err)
	}
	return nil
}
