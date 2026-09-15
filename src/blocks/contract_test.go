// Contract test: builds the block binaries, runs a small topology as plain
// processes (no Docker) and drives it through the admin API the way the
// engine will. It is the executable form of the contract in README.md.
package blocks_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var bins = map[string]string{}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "symphony-blocks")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"gateway", "service", "broker", "database", "proxy"} {
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./"+name)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build", name, err)
			os.Exit(1)
		}
		bins[name] = out
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type proc struct {
	name        string
	data, admin int
	cmd         *exec.Cmd
	token       string
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type nodeSpec struct {
	block  string
	node   string
	links  map[string]any
	config map[string]any
}

// start launches a block with the given links and returns it once healthy.
func start(t *testing.T, spec nodeSpec, ports map[string]int) *proc {
	t.Helper()
	p := &proc{name: spec.node, token: "test-token"}
	p.data = ports[spec.node]
	p.admin = freePort(t)
	links, _ := json.Marshal(spec.links)
	cfg, _ := json.Marshal(spec.config)
	p.cmd = exec.Command(bins[spec.block])
	p.cmd.Env = append(os.Environ(),
		"SYMPHONY_NODE="+spec.node,
		"SYMPHONY_KIND="+kindOf(spec.block),
		"SYMPHONY_DEPLOYMENT=test",
		"SYMPHONY_DATA_PORT="+strconv.Itoa(p.data),
		"SYMPHONY_ADMIN_PORT="+strconv.Itoa(p.admin),
		"SYMPHONY_ADMIN_TOKEN="+p.token,
		"SYMPHONY_LINKS="+string(links),
		"SYMPHONY_CONFIG="+string(cfg),
	)
	if testing.Verbose() {
		p.cmd.Stdout = os.Stderr
		p.cmd.Stderr = os.Stderr
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h, err := p.health(); err == nil && h["status"] == "healthy" {
			return p
		}
		time.Sleep(50 * time.Millisecond)
	}
	h, err := p.health()
	t.Fatalf("%s never became healthy: %v %v", spec.node, h, err)
	return nil
}

func kindOf(block string) string {
	switch block {
	case "gateway":
		return "infra"
	case "database":
		return "db"
	}
	return block
}

func (p *proc) adminURL() string { return fmt.Sprintf("http://127.0.0.1:%d", p.admin) }
func (p *proc) dataURL() string  { return fmt.Sprintf("http://127.0.0.1:%d", p.data) }

func (p *proc) get(path string) (map[string]any, error) {
	req, _ := http.NewRequest(http.MethodGet, p.adminURL()+path, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (p *proc) health() (map[string]any, error) { return p.get("/health") }

func (p *proc) control(t *testing.T, cmd map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req, _ := http.NewRequest(http.MethodPost, p.adminURL()+"/control", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s control %v: %v", p.name, cmd["op"], err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("%s control %v failed: %v", p.name, cmd["op"], out["error"])
	}
	return out
}

func (p *proc) counter(t *testing.T, name string, labels map[string]string) int64 {
	t.Helper()
	doc, err := p.get("/metrics")
	if err != nil {
		t.Fatal(err)
	}
	counters, _ := doc["counters"].([]any)
	var sum int64
	for _, c := range counters {
		m := c.(map[string]any)
		if m["name"] != name {
			continue
		}
		lb, _ := m["labels"].(map[string]any)
		match := true
		for k, v := range labels {
			if lb[k] != v {
				match = false
			}
		}
		if match {
			sum += int64(m["value"].(float64))
		}
	}
	return sum
}

func link(edge, kind, from, to string, targets ...string) map[string]any {
	l := map[string]any{"edge": edge, "kind": kind, "from": from, "to": to}
	if len(targets) > 0 {
		l["targets"] = targets
	}
	return l
}

func withTopic(l map[string]any, topic string) map[string]any {
	l["topic"] = topic
	return l
}

func url(ports map[string]int, node string) string {
	return fmt.Sprintf("http://127.0.0.1:%d", ports[node])
}

// TestOrchestration: gateway -> orchestrator -> proxy -> payment, orchestrator -> db.
func TestOrchestration(t *testing.T) {
	ports := map[string]int{}
	for _, n := range []string{"gateway", "orchestrator", "toxiproxy", "payment", "postgres"} {
		ports[n] = freePort(t)
	}
	db := start(t, nodeSpec{block: "database", node: "postgres",
		links:  map[string]any{"in": []any{link("e-orch-db", "uses", "orchestrator", "postgres")}},
		config: map[string]any{"max_connections": 4, "write_ms": 1, "fsync_ms": 1}}, ports)
	pay := start(t, nodeSpec{block: "service", node: "payment",
		links: map[string]any{"in": []any{link("e-proxy-pay", "calls", "toxiproxy", "payment")}}}, ports)
	prx := start(t, nodeSpec{block: "proxy", node: "toxiproxy",
		links: map[string]any{
			"in":    []any{link("e-orch-proxy", "calls", "orchestrator", "toxiproxy")},
			"out":   []any{link("e-proxy-pay", "calls", "toxiproxy", "payment", url(ports, "payment"))},
			"pairs": []any{map[string]any{"in": "e-orch-proxy", "out": "e-proxy-pay"}},
		}}, ports)
	orch := start(t, nodeSpec{block: "service", node: "orchestrator",
		links: map[string]any{
			"in": []any{link("e-gw-orch", "calls", "gateway", "orchestrator")},
			"out": []any{
				link("e-orch-proxy", "calls", "orchestrator", "toxiproxy", url(ports, "toxiproxy")),
				link("e-orch-db", "uses", "orchestrator", "postgres", url(ports, "postgres")),
			},
		}, config: map[string]any{"timeout_ms": 1500, "retry_max": 1}}, ports)
	gw := start(t, nodeSpec{block: "gateway", node: "gateway",
		links:  map[string]any{"out": []any{link("e-gw-orch", "calls", "gateway", "orchestrator", url(ports, "orchestrator"))}},
		config: map[string]any{"saga_timeout_ms": 3000}}, ports)

	// one saga through /ingress
	resp, err := http.Post(gw.dataURL()+"/ingress", "application/json", bytes.NewReader([]byte(`{"order":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ingress: %d %s", resp.StatusCode, body)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["outcome"] != "ok" {
		t.Fatalf("expected ok outcome, got %s", body)
	}
	if n := pay.counter(t, "link_results_total", map[string]string{"edge": "e-proxy-pay", "dir": "in", "result": "ok"}); n != 1 {
		t.Fatalf("payment should have seen 1 ok request via the proxy, saw %d", n)
	}
	if n := db.counter(t, "db_ops_total", map[string]string{"op": "put", "result": "ok"}); n < 2 {
		t.Fatalf("db should have 2 puts (started, done), got %d", n)
	}

	// load for a bit
	gw.control(t, map[string]any{"op": "load.set", "params": map[string]any{"rps": 40, "concurrency": 32}, "duration_ms": 1500})
	time.Sleep(2200 * time.Millisecond)
	okc := gw.counter(t, "saga_total", map[string]string{"outcome": "ok"})
	if okc < 30 {
		t.Fatalf("expected >= 30 ok sagas from load, got %d", okc)
	}
	failed := gw.counter(t, "saga_total", map[string]string{"outcome": "failed"}) + gw.counter(t, "saga_total", map[string]string{"outcome": "compensated"})
	if failed > okc/10 {
		t.Fatalf("too many failures under normal load: %d failed vs %d ok", failed, okc)
	}

	// drop everything on the proxied link: sagas must compensate
	f := prx.control(t, map[string]any{"op": "fault.set", "target": map[string]any{"edge": "e-proxy-pay", "direction": "out"}, "params": map[string]any{"kind": "drop", "rate": 1.0}})
	fid := f["state"].(map[string]any)["id"].(string)
	before := gw.counter(t, "saga_total", map[string]string{"outcome": "compensated"})
	for i := 0; i < 8; i++ {
		resp, _ = http.Post(gw.dataURL()+"/ingress", "application/json", bytes.NewReader([]byte(`{}`)))
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatalf("saga should fail while the link drops: %s", body)
		}
	}
	if after := gw.counter(t, "saga_total", map[string]string{"outcome": "compensated"}); after != before+8 {
		t.Fatalf("expected 8 compensated sagas, before=%d after=%d body=%s", before, after, body)
	}
	if h, _ := orch.health(); h["status"] == "healthy" {
		t.Fatalf("orchestrator should report degraded after failures: %v", h["status"])
	}
	if h, _ := prx.health(); h["status"] != "degraded" {
		t.Fatalf("proxy with an active fault should be degraded, got %v", h["status"])
	}
	prx.control(t, map[string]any{"op": "fault.clear", "params": map[string]any{"id": fid}})

	// hot config and pool exhaustion on the database
	db.control(t, map[string]any{"op": "pool.set", "params": map[string]any{"max_connections": 1, "statement_timeout_ms": 50}})
	db.control(t, map[string]any{"op": "latency.set", "params": map[string]any{"write_ms": 200}})
	gw.control(t, map[string]any{"op": "load.set", "params": map[string]any{"rps": 30, "concurrency": 16}, "duration_ms": 800})
	time.Sleep(1500 * time.Millisecond)
	if n := db.counter(t, "db_ops_total", map[string]string{"result": "pool_exhausted"}); n == 0 {
		t.Fatal("expected pool_exhausted errors with 1 connection and 200ms writes")
	}
	if h, _ := db.health(); h["status"] == "healthy" {
		t.Fatalf("db should not be healthy while its pool is exhausted: %v", h)
	}

	// chaos: stall makes the node failing but the admin port keeps answering
	pay.control(t, map[string]any{"op": "chaos.stall", "duration_ms": 300})
	if h, _ := pay.health(); h["status"] != "failing" {
		t.Fatalf("stalled block should be failing, got %v", h["status"])
	}
	time.Sleep(400 * time.Millisecond)
	pay.control(t, map[string]any{"op": "state.reset"})
	if h, _ := pay.health(); h["status"] != "healthy" {
		t.Fatalf("after reset payment should be healthy, got %v", h)
	}
	// wrong token is rejected
	req, _ := http.NewRequest(http.MethodGet, pay.adminURL()+"/health", nil)
	req.Header.Set("Authorization", "Bearer nope")
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 401 {
		t.Fatalf("expected 401 with a wrong token, got %d", r2.StatusCode)
	}
	r2.Body.Close()
}

// TestChoreography: gateway -> order -> kafka -> (proxy) -> payment -> kafka -> inventory (sink).
func TestChoreography(t *testing.T) {
	ports := map[string]int{}
	for _, n := range []string{"gateway", "order", "kafka", "toxiproxy", "payment", "inventory"} {
		ports[n] = freePort(t)
	}
	gwAdmin := freePort(t)
	_ = gwAdmin
	inv := start(t, nodeSpec{block: "service", node: "inventory",
		links: map[string]any{"in": []any{withTopic(link("e-inv-sub", "subscribes", "kafka", "inventory"), "payment.completed")}}}, ports)
	pay := start(t, nodeSpec{block: "service", node: "payment",
		links: map[string]any{
			"in":  []any{withTopic(link("e-pay-sub", "subscribes", "toxiproxy", "payment"), "order.created")},
			"out": []any{withTopic(link("e-pay-pub", "publishes", "payment", "kafka", url(ports, "kafka")), "payment.completed")},
		}}, ports)
	prx := start(t, nodeSpec{block: "proxy", node: "toxiproxy",
		links: map[string]any{
			"in":    []any{withTopic(link("e-proxy-sub", "subscribes", "kafka", "toxiproxy"), "order.created")},
			"out":   []any{withTopic(link("e-pay-sub", "subscribes", "toxiproxy", "payment", url(ports, "payment")), "order.created")},
			"pairs": []any{map[string]any{"in": "e-proxy-sub", "out": "e-pay-sub"}},
		}}, ports)
	kafka := start(t, nodeSpec{block: "broker", node: "kafka",
		links: map[string]any{
			"in": []any{
				withTopic(link("e-order-pub", "publishes", "order", "kafka"), "order.created"),
				withTopic(link("e-pay-pub", "publishes", "payment", "kafka"), "payment.completed"),
			},
			"out": []any{
				withTopic(link("e-proxy-sub", "subscribes", "kafka", "toxiproxy", url(ports, "toxiproxy")), "order.created"),
				withTopic(link("e-inv-sub", "subscribes", "kafka", "inventory", url(ports, "inventory")), "payment.completed"),
			},
		}}, ports)
	order := start(t, nodeSpec{block: "service", node: "order",
		links: map[string]any{
			"in":  []any{link("e-gw-order", "calls", "gateway", "order")},
			"out": []any{withTopic(link("e-order-pub", "publishes", "order", "kafka", url(ports, "kafka")), "order.created")},
		}}, ports)
	_ = order
	_ = prx
	gw := start(t, nodeSpec{block: "gateway", node: "gateway",
		links:  map[string]any{"out": []any{link("e-gw-order", "calls", "gateway", "order", url(ports, "order"))}},
		config: map[string]any{"saga_timeout_ms": 4000}}, ports)
	// the sink reports outcomes to the gateway: tell it where
	// (in the real deployment the engine puts it into SYMPHONY_LINKS.gateway)
	restart := func() {}
	_ = restart

	// without a gateway URL the sink cannot report; verify the pipeline first
	resp, err := http.Post(gw.dataURL()+"/ingress", "application/json", bytes.NewReader([]byte(`{"order":2}`)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["outcome"] != "pending" {
		t.Fatalf("choreography entry should reply pending, got %s", body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if inv.counter(t, "link_results_total", map[string]string{"edge": "e-inv-sub", "dir": "in", "result": "ok"}) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := inv.counter(t, "link_results_total", map[string]string{"edge": "e-inv-sub", "dir": "in", "result": "ok"}); n < 1 {
		t.Fatalf("inventory never received the payment.completed event (broker delivery through proxy failed); kafka health: %v", must(kafka.health()))
	}
	if n := pay.counter(t, "link_results_total", map[string]string{"edge": "e-pay-sub", "dir": "in", "result": "ok"}); n < 1 {
		t.Fatalf("payment never received order.created via proxy")
	}
	// lag must drain to zero
	time.Sleep(200 * time.Millisecond)
	topics := kafka.control(t, map[string]any{"op": "topics.get"})
	t.Logf("topics: %v", topics["state"])
	if h, _ := kafka.health(); h["status"] != "healthy" {
		t.Fatalf("broker should be healthy with drained lag: %v", h)
	}
	// pause the consumer: lag builds, health degrades
	kafka.control(t, map[string]any{"op": "consumer.pause", "params": map[string]any{"group": "e-inv-sub"}})
	for i := 0; i < 5; i++ {
		http.Post(gw.dataURL()+"/ingress", "application/json", bytes.NewReader([]byte(`{}`)))
	}
	time.Sleep(800 * time.Millisecond)
	lagged := false
	h, _ := kafka.health()
	for _, c := range h["checks"].([]any) {
		cm := c.(map[string]any)
		if cm["name"] == "lag:e-inv-sub" {
			if d := cm["detail"].(map[string]any); d["lag"].(float64) >= 1 && d["paused"] == true {
				lagged = true
			}
		}
	}
	if !lagged {
		t.Fatalf("expected lag on paused group: %v", h)
	}
	kafka.control(t, map[string]any{"op": "consumer.resume", "params": map[string]any{"group": "e-inv-sub"}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if inv.counter(t, "link_results_total", map[string]string{"edge": "e-inv-sub", "dir": "in", "result": "ok"}) >= 6 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := inv.counter(t, "link_results_total", map[string]string{"edge": "e-inv-sub", "dir": "in", "result": "ok"}); n < 6 {
		t.Fatalf("after resume the backlog should drain, inventory saw %d", n)
	}
}

func must(m map[string]any, _ error) map[string]any { return m }
