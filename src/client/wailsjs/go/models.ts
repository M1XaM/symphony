export namespace events {
	
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

