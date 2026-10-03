// The topology commands: templates, validate, plan, apply, teardown, restore.

package cli

import (
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"time"

	"symphony/engine/spec"
)

func cmdTemplates(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	type row struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Nodes   int    `json:"nodes"`
		Edges   int    `json:"edges"`
		Applied bool   `json:"applied"`
	}
	applied := map[string]bool{}
	if recs, err := e.b.Store.Deployments(); err == nil {
		for _, r := range recs {
			applied[r.Name] = true
		}
	}
	var out []row
	for _, t := range e.a.GetPresets() {
		out = append(out, row{t.Name, "built-in", len(t.Nodes), len(t.Edges), applied[t.Name]})
	}
	saved, err := e.a.GetUserTemplates()
	if err != nil {
		return err
	}
	for _, t := range saved {
		out = append(out, row{t.Name, "saved", len(t.Nodes), len(t.Edges), applied[t.Name]})
	}
	if e.json {
		return e.printJSON(out)
	}
	rows := make([][]string, len(out))
	for i, r := range out {
		mark := ""
		if r.Applied {
			mark = "applied"
		}
		rows[i] = []string{r.Name, r.Source, fmt.Sprint(r.Nodes), fmt.Sprint(r.Edges), mark}
	}
	e.table("TEMPLATE\tSOURCE\tNODES\tEDGES\t", rows)
	return nil
}

func cmdShow(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(args[0])
	if err != nil {
		return err
	}
	return e.printJSON(t)
}

func cmdSave(e *env, args []string) error {
	fset := flag.NewFlagSet("save", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("needs one file")
	}
	if pos[0] != "-" && !fileExists(pos[0]) {
		return fmt.Errorf("%s: no such file", pos[0])
	}
	t, err := e.topology(pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	if _, err := e.a.SaveUserTemplate(t); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "saved template %q\n", t.Name)
	return nil
}

func cmdGenerate(e *env, args []string) error {
	fset := flag.NewFlagSet("generate", flag.ContinueOnError)
	bias := fset.Float64("bias", 0.5, "")
	name := fset.String("name", "Generated", "")
	seed := fset.Int64("seed", 0, "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("takes no arguments")
	}
	if *bias < 0 || *bias > 1 {
		return usagef("--bias is between 0 and 1")
	}
	if !spec.ValidName(*name) || *name == "" {
		return usagef("--name takes English letters, digits, spaces and special characters")
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	return e.printJSON(spec.Generate(*bias, *name, rand.New(rand.NewSource(*seed))))
}

func cmdDelete(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one template name")
	}
	before, err := e.a.GetUserTemplates()
	if err != nil {
		return err
	}
	after, err := e.a.DeleteUserTemplate(args[0])
	if err != nil {
		return err
	}
	if len(after) == len(before) {
		return fmt.Errorf("no saved template %q (built-in ones cannot be deleted)", args[0])
	}
	fmt.Fprintf(e.out, "deleted template %q\n", args[0])
	return nil
}

func cmdValidate(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(args[0])
	if err != nil {
		return err
	}
	findings := e.a.ValidateTopology(t)
	if e.json {
		if err := e.printJSON(findings); err != nil {
			return err
		}
	} else if len(findings) == 0 {
		fmt.Fprintf(e.out, "%q is valid\n", t.Name)
	} else {
		for _, f := range findings {
			fmt.Fprintf(e.out, "%-7s %s\n", f.Severity, f.Error())
		}
	}
	if spec.HasErrors(findings) {
		return errors.New("the topology has errors")
	}
	return nil
}

func cmdPlan(e *env, args []string) error {
	fset := flag.NewFlagSet("plan", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	plan, err := e.a.PlanApply(t)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(plan)
	}
	if !e.docker.Available {
		fmt.Fprintln(e.out, "(Docker is not available: planned as if nothing were running)")
	}
	rows := make([][]string, len(plan.Ops))
	for i, op := range plan.Ops {
		rows[i] = []string{fmt.Sprint(i + 1), string(op.Kind), op.Target, string(op.Class), op.Reason}
	}
	e.table("#\tOPERATION\tTARGET\tCLASS\tREASON", rows)
	s := plan.Summary
	fmt.Fprintf(e.out, "\n%d operations: %d hot, %d cold, %d structural (%d creates, %d destroys)\n",
		len(plan.Ops), s.Hot, s.Cold, s.Structural, s.Creates, s.Destroys)
	return nil
}

func cmdApply(e *env, args []string) error {
	fset := flag.NewFlagSet("apply", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	fmt.Fprintf(e.out, "applying %q\n", t.Name)
	defer e.follow(t.Name)()
	start := time.Now()
	if err := e.a.Apply(t); err != nil {
		return err
	}
	d, err := e.deployment(t.Name)
	if err != nil {
		fmt.Fprintf(e.out, "%q applied: nothing left to run\n", t.Name)
		return nil
	}
	up, total := ready(d)
	fmt.Fprintf(e.out, "%q is %s: %d/%d nodes up (%s)\n", d.Name, d.Phase, up, total, time.Since(start).Round(100*time.Millisecond))
	return nil
}

func cmdTeardown(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one deployment")
	}
	d, err := e.deployment(args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "tearing down %q\n", d.Name)
	defer e.follow(d.Name)()
	if err := e.a.Teardown(d.Name); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%q is gone\n", d.Name)
	return nil
}

func cmdRestore(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	broken, err := e.b.Engine.Attach(e.ctx)
	if err != nil {
		return err
	}
	e.b.Engine.CollectOrphans(e.ctx)
	// faults that became due while nothing was running to end them
	e.b.Engine.ResumeFaults(broken)
	if len(broken) == 0 {
		fmt.Fprintln(e.out, "every stored deployment is running as stored")
		return nil
	}
	failed := 0
	for _, name := range broken {
		fmt.Fprintf(e.out, "reopening %q\n", name)
		stop := e.follow(name)
		err := e.b.Engine.Reopen(e.ctx, name)
		stop()
		if err != nil {
			fmt.Fprintf(e.out, "%q: %v\n", name, err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d deployment(s) could not be reopened", failed, len(broken))
	}
	return nil
}
