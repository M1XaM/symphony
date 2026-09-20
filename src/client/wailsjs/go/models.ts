export namespace api {
	
	export class LogStreamState {
	    available: boolean;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LogStreamState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.enabled = source["enabled"];
	    }
	}
	export class RuntimeInfo {
	    kind: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new RuntimeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.detail = source["detail"];
	    }
	}

}

export namespace blockclient {
	
	export class LogLine {
	    ts: number;
	    level: string;
	    msg: string;
	    attrs?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new LogLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ts = source["ts"];
	        this.level = source["level"];
	        this.msg = source["msg"];
	        this.attrs = source["attrs"];
	    }
	}

}

export namespace control {
	
	export class Command {
	    id?: string;
	    op: string;
	    target?: faults.Target;
	    params?: Record<string, any>;
	    duration_ms?: number;
	
	    static createFrom(source: any = {}) {
	        return new Command(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.op = source["op"];
	        this.target = this.convertValues(source["target"], faults.Target);
	        this.params = source["params"];
	        this.duration_ms = source["duration_ms"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Result {
	    id?: string;
	    op: string;
	    ok: boolean;
	    error?: string;
	    state?: any;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.op = source["op"];
	        this.ok = source["ok"];
	        this.error = source["error"];
	        this.state = source["state"];
	    }
	}

}

export namespace core {
	
	export class NodeStatus {
	    node_id: string;
	    state: string;
	    replicas: number;
	    ready: number;
	    message?: string;
	    updated_at: number;
	    checks?: events.HealthCheck[];
	
	    static createFrom(source: any = {}) {
	        return new NodeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_id = source["node_id"];
	        this.state = source["state"];
	        this.replicas = source["replicas"];
	        this.ready = source["ready"];
	        this.message = source["message"];
	        this.updated_at = source["updated_at"];
	        this.checks = this.convertValues(source["checks"], events.HealthCheck);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DeploymentInfo {
	    name: string;
	    phase: string;
	    network: string;
	    topology: spec.Topology;
	    statuses: Record<string, NodeStatus>;
	
	    static createFrom(source: any = {}) {
	        return new DeploymentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.phase = source["phase"];
	        this.network = source["network"];
	        this.topology = this.convertValues(source["topology"], spec.Topology);
	        this.statuses = this.convertValues(source["statuses"], NodeStatus, true);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace events {
	
	export class HealthCheck {
	    name: string;
	    status: string;
	    detail?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new HealthCheck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	    }
	}
	export class HostStatsPayload {
	    ts: number;
	    cpu_pct: number;
	    mem_pct: number;
	    mem_used_bytes: number;
	    mem_total_bytes: number;
	    net_rx_bps: number;
	    net_tx_bps: number;
	
	    static createFrom(source: any = {}) {
	        return new HostStatsPayload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ts = source["ts"];
	        this.cpu_pct = source["cpu_pct"];
	        this.mem_pct = source["mem_pct"];
	        this.mem_used_bytes = source["mem_used_bytes"];
	        this.mem_total_bytes = source["mem_total_bytes"];
	        this.net_rx_bps = source["net_rx_bps"];
	        this.net_tx_bps = source["net_tx_bps"];
	    }
	}
	export class LogPayload {
	    level: string;
	    source: string;
	    message: string;
	    ts: number;
	
	    static createFrom(source: any = {}) {
	        return new LogPayload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.level = source["level"];
	        this.source = source["source"];
	        this.message = source["message"];
	        this.ts = source["ts"];
	    }
	}
	export class MetricsPayload {
	    deployment: string;
	    ts: number;
	    latency_ms: number;
	    p99_ms: number;
	    consumer_lag: number;
	    sagas_per_sec: number;
	    error_rate: number;
	    cpu_pct: number;
	    active_faults: number;
	    sagas_ok: number;
	    sagas_failed: number;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new MetricsPayload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deployment = source["deployment"];
	        this.ts = source["ts"];
	        this.latency_ms = source["latency_ms"];
	        this.p99_ms = source["p99_ms"];
	        this.consumer_lag = source["consumer_lag"];
	        this.sagas_per_sec = source["sagas_per_sec"];
	        this.error_rate = source["error_rate"];
	        this.cpu_pct = source["cpu_pct"];
	        this.active_faults = source["active_faults"];
	        this.sagas_ok = source["sagas_ok"];
	        this.sagas_failed = source["sagas_failed"];
	        this.source = source["source"];
	    }
	}

}

export namespace faults {
	
	export class Target {
	    edge?: string;
	    direction?: string;
	    peer?: string;
	    topic?: string;
	    table?: string;
	
	    static createFrom(source: any = {}) {
	        return new Target(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.edge = source["edge"];
	        this.direction = source["direction"];
	        this.peer = source["peer"];
	        this.topic = source["topic"];
	        this.table = source["table"];
	    }
	}

}

export namespace planner {
	
	export class Operation {
	    id: string;
	    kind: string;
	    target: string;
	    class: string;
	    reason: string;
	    params?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new Operation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.target = source["target"];
	        this.class = source["class"];
	        this.reason = source["reason"];
	        this.params = source["params"];
	    }
	}
	export class Summary {
	    hot: number;
	    cold: number;
	    structural: number;
	    creates: number;
	    destroys: number;
	    controls: number;
	
	    static createFrom(source: any = {}) {
	        return new Summary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hot = source["hot"];
	        this.cold = source["cold"];
	        this.structural = source["structural"];
	        this.creates = source["creates"];
	        this.destroys = source["destroys"];
	        this.controls = source["controls"];
	    }
	}
	export class Plan {
	    ops: Operation[];
	    summary: Summary;
	    network: string;
	
	    static createFrom(source: any = {}) {
	        return new Plan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ops = this.convertValues(source["ops"], Operation);
	        this.summary = this.convertValues(source["summary"], Summary);
	        this.network = source["network"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace runtime {
	
	export class HostContainer {
	    id: string;
	    name: string;
	    image: string;
	    command: string;
	    created_at: number;
	    state: string;
	    status: string;
	    health?: string;
	    ports: string[];
	    networks: string[];
	    ours: boolean;
	    deployment?: string;
	    node?: string;
	
	    static createFrom(source: any = {}) {
	        return new HostContainer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.image = source["image"];
	        this.command = source["command"];
	        this.created_at = source["created_at"];
	        this.state = source["state"];
	        this.status = source["status"];
	        this.health = source["health"];
	        this.ports = source["ports"];
	        this.networks = source["networks"];
	        this.ours = source["ours"];
	        this.deployment = source["deployment"];
	        this.node = source["node"];
	    }
	}

}

export namespace spec {
	
	export class Config {
	    hot: Record<string, any>;
	    cold: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hot = source["hot"];
	        this.cold = source["cold"];
	    }
	}
	export class Edge {
	    id: string;
	    from: string;
	    to: string;
	    kind: string;
	    topic?: string;
	    via?: string;
	    timeout_ms?: number;
	    retries?: number;
	
	    static createFrom(source: any = {}) {
	        return new Edge(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.kind = source["kind"];
	        this.topic = source["topic"];
	        this.via = source["via"];
	        this.timeout_ms = source["timeout_ms"];
	        this.retries = source["retries"];
	    }
	}
	export class FaultSpec {
	    kind: string;
	    duration_ms: number;
	    params: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new FaultSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.duration_ms = source["duration_ms"];
	        this.params = source["params"];
	    }
	}
	export class Resources {
	    cpus?: number;
	    memory_mb?: number;
	    pids_max?: number;
	
	    static createFrom(source: any = {}) {
	        return new Resources(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cpus = source["cpus"];
	        this.memory_mb = source["memory_mb"];
	        this.pids_max = source["pids_max"];
	    }
	}
	export class Position {
	    x: number;
	    y: number;
	
	    static createFrom(source: any = {}) {
	        return new Position(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.y = source["y"];
	    }
	}
	export class Node {
	    id: string;
	    kind: string;
	    image: string;
	    replicas: number;
	    position: Position;
	    config: Config;
	    resources: Resources;
	    behaviour?: number[];
	
	    static createFrom(source: any = {}) {
	        return new Node(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.image = source["image"];
	        this.replicas = source["replicas"];
	        this.position = this.convertValues(source["position"], Position);
	        this.config = this.convertValues(source["config"], Config);
	        this.resources = this.convertValues(source["resources"], Resources);
	        this.behaviour = source["behaviour"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class Topology {
	    version: number;
	    name: string;
	    nodes: Node[];
	    edges: Edge[];
	
	    static createFrom(source: any = {}) {
	        return new Topology(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.name = source["name"];
	        this.nodes = this.convertValues(source["nodes"], Node);
	        this.edges = this.convertValues(source["edges"], Edge);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ValidationError {
	    path: string;
	    node?: string;
	    edge?: string;
	    message: string;
	    severity: string;
	
	    static createFrom(source: any = {}) {
	        return new ValidationError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.node = source["node"];
	        this.edge = source["edge"];
	        this.message = source["message"];
	        this.severity = source["severity"];
	    }
	}

}

export namespace tests {
	
	export class Assertion {
	    metric: string;
	    op: string;
	    value: number;
	    phase?: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new Assertion(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.metric = source["metric"];
	        this.op = source["op"];
	        this.value = source["value"];
	        this.phase = source["phase"];
	        this.note = source["note"];
	    }
	}
	export class AssertionResult {
	    metric: string;
	    op: string;
	    value: number;
	    phase?: string;
	    note?: string;
	    actual: number;
	    passed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AssertionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.metric = source["metric"];
	        this.op = source["op"];
	        this.value = source["value"];
	        this.phase = source["phase"];
	        this.note = source["note"];
	        this.actual = source["actual"];
	        this.passed = source["passed"];
	    }
	}
	export class PhaseSpan {
	    name: string;
	    start_s: number;
	    end_s: number;
	
	    static createFrom(source: any = {}) {
	        return new PhaseSpan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.start_s = source["start_s"];
	        this.end_s = source["end_s"];
	    }
	}
	export class Point {
	    t: number;
	    v: number;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.t = source["t"];
	        this.v = source["v"];
	    }
	}
	export class Requires {
	    kinds?: string[];
	    needs_proxy?: boolean;
	    needs_broker?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Requires(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kinds = source["kinds"];
	        this.needs_proxy = source["needs_proxy"];
	        this.needs_broker = source["needs_broker"];
	    }
	}
	export class Run {
	    id: string;
	    scenario: string;
	    name: string;
	    template: string;
	    topology: spec.Topology;
	    params: Record<string, any>;
	    runtime: string;
	    started_at: number;
	    ended_at: number;
	    passed: boolean;
	    aborted: boolean;
	    error?: string;
	    phases: PhaseSpan[];
	    series: Record<string, Array<Point>>;
	    summary: Record<string, number>;
	    assertions: AssertionResult[];
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new Run(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.scenario = source["scenario"];
	        this.name = source["name"];
	        this.template = source["template"];
	        this.topology = this.convertValues(source["topology"], spec.Topology);
	        this.params = source["params"];
	        this.runtime = source["runtime"];
	        this.started_at = source["started_at"];
	        this.ended_at = source["ended_at"];
	        this.passed = source["passed"];
	        this.aborted = source["aborted"];
	        this.error = source["error"];
	        this.phases = this.convertValues(source["phases"], PhaseSpan);
	        this.series = this.convertValues(source["series"], Array<Point>, true);
	        this.summary = source["summary"];
	        this.assertions = this.convertValues(source["assertions"], AssertionResult);
	        this.notes = source["notes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RunSummary {
	    id: string;
	    scenario: string;
	    name: string;
	    template: string;
	    started_at: number;
	    ended_at: number;
	    passed: boolean;
	    aborted: boolean;
	    error?: string;
	    summary: Record<string, number>;
	    running: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RunSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.scenario = source["scenario"];
	        this.name = source["name"];
	        this.template = source["template"];
	        this.started_at = source["started_at"];
	        this.ended_at = source["ended_at"];
	        this.passed = source["passed"];
	        this.aborted = source["aborted"];
	        this.error = source["error"];
	        this.summary = source["summary"];
	        this.running = source["running"];
	    }
	}
	export class Step {
	    at_s: any;
	    do: string;
	    phase?: string;
	    node?: string;
	    kind?: string;
	    edge?: string;
	    args?: Record<string, any>;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new Step(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at_s = source["at_s"];
	        this.do = source["do"];
	        this.phase = source["phase"];
	        this.node = source["node"];
	        this.kind = source["kind"];
	        this.edge = source["edge"];
	        this.args = source["args"];
	        this.note = source["note"];
	    }
	}
	export class Scenario {
	    id: string;
	    name: string;
	    description: string;
	    tags?: string[];
	    requires: Requires;
	    params: Record<string, any>;
	    timeline: Step[];
	    collect?: string[];
	    assert?: Assertion[];
	    reveals?: string;
	
	    static createFrom(source: any = {}) {
	        return new Scenario(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.tags = source["tags"];
	        this.requires = this.convertValues(source["requires"], Requires);
	        this.params = source["params"];
	        this.timeline = this.convertValues(source["timeline"], Step);
	        this.collect = source["collect"];
	        this.assert = this.convertValues(source["assert"], Assertion);
	        this.reveals = source["reveals"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

