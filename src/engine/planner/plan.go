// Package planner computes the ordered list of operations that turns the
// currently running topology into the desired one. It is pure: no runtime
// access, no side effects, deterministic output for the same inputs.
package planner

import (
	"encoding/json"
	"fmt"
	"sort"

	"symphony/engine/spec"
)

// OpKind is the primitive the engine knows how to execute.
type OpKind string

const (
	OpEnsureImage     OpKind = "ensure_image" // pull or build a block image
	OpEnsureNetwork   OpKind = "ensure_network"
	OpCreate          OpKind = "create"
	OpWaitHealthy     OpKind = "wait_healthy"
	OpEnsureTopic     OpKind = "ensure_topic"
	OpControl         OpKind = "control"          // push hot config to a running node
	OpConfigure       OpKind = "configure"        // push a behaviour program to a running node
	OpUpdateResources OpKind = "update_resources" // change CPU/RAM limits in place
	OpDestroy         OpKind = "destroy"
)

// ChangeClass explains why an operation is in the plan and how disruptive
// it is. Hot changes touch running containers in place; cold changes restart
// a node; structural changes add or remove nodes, topics or networks.
type ChangeClass string

const (
	ClassHot        ChangeClass = "hot"
	ClassCold       ChangeClass = "cold"
	ClassStructural ChangeClass = "structural"
)

// Operation is one step of a Plan. Target is a node id for node-scoped ops,
// a network name for ensure_network and "<broker>/<topic>" for ensure_topic.
type Operation struct {
	ID     string         `json:"id"`
	Kind   OpKind         `json:"kind"`
	Target string         `json:"target"`
	Class  ChangeClass    `json:"class"`
	Reason string         `json:"reason"`
	Params map[string]any `json:"params,omitempty"`
}

// Summary counts operations by class and by effect.
type Summary struct {
	Hot        int `json:"hot"`
	Cold       int `json:"cold"`
	Structural int `json:"structural"`
	Creates    int `json:"creates"`
	Destroys   int `json:"destroys"`
	Controls   int `json:"controls"`
}

// Plan is an ordered operation list plus its summary.
type Plan struct {
	Ops     []Operation `json:"ops"`
	Summary Summary     `json:"summary"`
	Network string      `json:"network"`
}

// NetworkName derives the container network for a topology. Each applied
// topology gets its own network, so several can run side by side without
// their containers seeing each other.
func NetworkName(t spec.Topology) string {
	return "symphony-" + spec.Slug(t.Name)
}

type builder struct {
	plan Plan
}

func (b *builder) add(kind OpKind, target string, class ChangeClass, reason string, params map[string]any) {
	op := Operation{
		ID:     fmt.Sprintf("op-%02d", len(b.plan.Ops)+1),
		Kind:   kind,
		Target: target,
		Class:  class,
		Reason: reason,
		Params: params,
	}
	b.plan.Ops = append(b.plan.Ops, op)
	switch class {
	case ClassHot:
		b.plan.Summary.Hot++
	case ClassCold:
		b.plan.Summary.Cold++
	case ClassStructural:
		b.plan.Summary.Structural++
	}
	switch kind {
	case OpCreate:
		b.plan.Summary.Creates++
	case OpDestroy:
		b.plan.Summary.Destroys++
	case OpControl, OpConfigure, OpUpdateResources:
		b.plan.Summary.Controls++
	}
}

// createNode emits the create + wait_healthy pair for one node.
func (b *builder) createNode(n spec.Node, class ChangeClass, reason string) {
	b.add(OpCreate, n.ID, class, reason, map[string]any{
		"image":    n.Image,
		"replicas": n.Replicas,
		"kind":     string(n.Kind),
	})
	b.add(OpWaitHealthy, n.ID, class, fmt.Sprintf("wait for %d/%d replicas healthy", n.Replicas, n.Replicas), nil)
}

// Diff computes the plan that moves `current` to `desired`. An empty current
// topology means nothing is running; an empty desired topology tears
// everything down.
func Diff(current, desired spec.Topology) Plan {
	current = current.Normalized()
	desired = desired.Normalized()

	b := &builder{}
	b.plan.Network = NetworkName(desired)

	cur := indexNodes(current)
	des := indexNodes(desired)
	slug := spec.Slug(desired.Name)

	type recreateItem struct {
		node   spec.Node
		reason string
	}
	var removed, added []spec.Node
	var recreate []recreateItem
	var controls []Operation

	for _, n := range current.Nodes {
		if _, ok := des[n.ID]; !ok {
			removed = append(removed, n)
		}
	}
	for _, n := range desired.Nodes {
		old, ok := cur[n.ID]
		if !ok {
			added = append(added, n)
			continue
		}
		if reason := coldDiff(old, n); reason != "" {
			recreate = append(recreate, recreateItem{node: n, reason: reason})
			continue
		}
		if LinkSignature(current, n.ID, slug) != LinkSignature(desired, n.ID, slug) {
			recreate = append(recreate, recreateItem{node: n, reason: "links changed"})
			continue
		}
		if patch := hotDiff(old, n); len(patch) > 0 {
			controls = append(controls, Operation{
				Kind:   OpControl,
				Target: n.ID,
				Class:  ClassHot,
				Reason: describePatch(patch),
				Params: patch,
			})
		}
		if old.Resources != n.Resources {
			controls = append(controls, Operation{
				Kind:   OpUpdateResources,
				Target: n.ID,
				Class:  ClassHot,
				Reason: fmt.Sprintf("resources cpus=%g memory_mb=%d pids_max=%d", n.Resources.CPUs, n.Resources.MemoryMB, n.Resources.PidsMax),
				Params: map[string]any{"cpus": n.Resources.CPUs, "memory_mb": n.Resources.MemoryMB, "pids_max": n.Resources.PidsMax},
			})
		}
		if string(old.Behaviour) != string(n.Behaviour) {
			var prog any
			if len(n.Behaviour) > 0 {
				prog = json.RawMessage(n.Behaviour)
			}
			controls = append(controls, Operation{
				Kind:   OpConfigure,
				Target: n.ID,
				Class:  ClassHot,
				Reason: "behaviour program changed",
				Params: map[string]any{"behaviour": prog},
			})
		}
	}

	sortNodes(removed)
	sortNodes(added)
	sort.SliceStable(recreate, func(i, j int) bool {
		return lessNode(recreate[i].node, recreate[j].node)
	})
	sort.SliceStable(controls, func(i, j int) bool { return controls[i].Target < controls[j].Target })

	// 1. Images and the network exist before anything is created.
	if len(added)+len(recreate) > 0 {
		seen := map[string]bool{}
		var images []string
		for _, n := range added {
			if !seen[n.Image] {
				seen[n.Image] = true
				images = append(images, n.Image)
			}
		}
		for _, r := range recreate {
			if !seen[r.node.Image] {
				seen[r.node.Image] = true
				images = append(images, r.node.Image)
			}
		}
		sort.Strings(images)
		for _, img := range images {
			b.add(OpEnsureImage, img, ClassStructural, "block image present locally", nil)
		}
		b.add(OpEnsureNetwork, b.plan.Network, ClassStructural, "container network for the topology", nil)
	}

	// 2. Removed nodes go first, services before their dependencies.
	for i := len(removed) - 1; i >= 0; i-- {
		n := removed[i]
		b.add(OpDestroy, n.ID, ClassStructural, "node removed from topology", nil)
	}

	// 3. Cold changes: destroy then recreate, in dependency order.
	for _, r := range recreate {
		b.add(OpDestroy, r.node.ID, ClassCold, r.reason, nil)
		b.createNode(r.node, ClassCold, "recreate: "+r.reason)
	}

	// 4. New nodes, grouped by start priority; topics are ensured right after
	//    the broker group so subscribers never start before their topics.
	curTopics := topicSet(current)
	topicsDone := false
	ensureTopics := func() {
		if topicsDone {
			return
		}
		topicsDone = true
		brokers := desired.Topics()
		ids := make([]string, 0, len(brokers))
		for id := range brokers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, broker := range ids {
			topics := brokers[broker]
			sort.Strings(topics)
			for _, topic := range topics {
				key := broker + "/" + topic
				if curTopics[key] {
					continue
				}
				b.add(OpEnsureTopic, key, ClassStructural, "topic declared by a messaging edge",
					map[string]any{"broker": broker, "topic": topic})
			}
		}
	}
	lastPrio := -1
	for _, n := range added {
		p := n.Kind.StartPriority()
		if lastPrio == spec.KindBroker.StartPriority() && p > lastPrio {
			ensureTopics()
		}
		b.createNode(n, ClassStructural, "new node")
		lastPrio = p
	}
	ensureTopics()

	// 5. Hot changes last, once everything is up.
	for _, op := range controls {
		b.add(op.Kind, op.Target, op.Class, op.Reason, op.Params)
	}

	if b.plan.Ops == nil {
		b.plan.Ops = []Operation{}
	}
	return b.plan
}

func indexNodes(t spec.Topology) map[string]spec.Node {
	m := make(map[string]spec.Node, len(t.Nodes))
	for _, n := range t.Nodes {
		m[n.ID] = n
	}
	return m
}

func topicSet(t spec.Topology) map[string]bool {
	out := map[string]bool{}
	for broker, topics := range t.Topics() {
		for _, tp := range topics {
			out[broker+"/"+tp] = true
		}
	}
	return out
}

func sortNodes(ns []spec.Node) {
	sort.SliceStable(ns, func(i, j int) bool { return lessNode(ns[i], ns[j]) })
}

func lessNode(a, b spec.Node) bool {
	pa, pb := a.Kind.StartPriority(), b.Kind.StartPriority()
	if pa != pb {
		return pa < pb
	}
	return a.ID < b.ID
}

// coldDiff returns a human-readable reason if the node must be recreated.
func coldDiff(old, new spec.Node) string {
	if old.Image != new.Image {
		return fmt.Sprintf("image %s -> %s", old.Image, new.Image)
	}
	if old.Replicas != new.Replicas {
		return fmt.Sprintf("replicas %d -> %d", old.Replicas, new.Replicas)
	}
	if old.Kind != new.Kind {
		return fmt.Sprintf("kind %s -> %s", old.Kind, new.Kind)
	}
	if patch := mapDiff(old.Config.Cold, new.Config.Cold); len(patch) > 0 {
		return "cold config: " + describePatch(patch)
	}
	return ""
}

// hotDiff returns the changed hot keys with their new values.
func hotDiff(old, new spec.Node) map[string]any {
	return mapDiff(old.Config.Hot, new.Config.Hot)
}

func mapDiff(old, new map[string]any) map[string]any {
	patch := map[string]any{}
	for k, v := range new {
		ov, ok := old[k]
		if !ok || spec.CanonValue(ov) != spec.CanonValue(v) {
			patch[k] = v
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			patch[k] = nil
		}
	}
	return patch
}

func describePatch(patch map[string]any) string {
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for i, k := range keys {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%s=%s", k, spec.CanonValue(patch[k]))
	}
	return s
}
